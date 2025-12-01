package buffer

import (
	"context"
	"maps"

	"github.com/google/uuid"
	"github.com/rudderlabs/rudder-server/jobsdb"
	"github.com/rudderlabs/rudder-server/utils/tx"
	"github.com/samber/lo"
)

func (b *jobsDBPartitionBuffer) Store(ctx context.Context, jobList []*jobsdb.JobT) error {
	b.flushMu.RLock()
	defer b.flushMu.RUnlock()

	bufferedPartitions := b.getBufferedPartitionsSnapshot()
	if len(bufferedPartitions) == 0 {
		return b.primaryWriteJobsDB.Store(ctx, jobList)
	}
	b.primaryWriteJobsDB.WithStoreSafeTx(ctx, func(tx jobsdb.StoreSafeTx) error {
		return b.withLatestBufferedPartitions(ctx, bufferedPartitions, tx.Tx(), func(bufferedPartitions map[string]struct{}) error {
			primaryJobs, bufferedJobs := b.spitJobs(jobList, bufferedPartitions)
			if len(primaryJobs) > 0 {
				if err := b.primaryWriteJobsDB.StoreInTx(ctx, tx, primaryJobs); err != nil {
					return err
				}
			}
			if len(bufferedJobs) > 0 {
				return b.bufferWriteJobsDB.WithStoreSafeTxFromTx(ctx, tx.Tx(), func(tx jobsdb.StoreSafeTx) error {
					return b.bufferWriteJobsDB.StoreInTx(ctx, tx, bufferedJobs)
				})
			}
			return nil
		})
	})
	return nil
}

func (b *jobsDBPartitionBuffer) StoreInTx(ctx context.Context, tx jobsdb.StoreSafeTx, jobList []*jobsdb.JobT) error {
	b.flushMu.RLock()
	defer b.flushMu.RUnlock()
	bufferedPartitions := b.getBufferedPartitionsSnapshot()
	if len(bufferedPartitions) == 0 {
		return b.primaryWriteJobsDB.StoreInTx(ctx, tx, jobList)
	}
	return b.withLatestBufferedPartitions(ctx, bufferedPartitions, tx.Tx(), func(bufferedPartitions map[string]struct{}) error {
		primaryJobs, bufferedJobs := b.spitJobs(jobList, bufferedPartitions)
		if len(primaryJobs) > 0 {
			if err := b.primaryWriteJobsDB.StoreInTx(ctx, tx, primaryJobs); err != nil {
				return err
			}
		}
		if len(bufferedJobs) > 0 {
			return b.bufferWriteJobsDB.WithStoreSafeTxFromTx(ctx, tx.Tx(), func(tx jobsdb.StoreSafeTx) error {
				return b.bufferWriteJobsDB.StoreInTx(ctx, tx, bufferedJobs)
			})
		}
		return nil
	})

}

func (b *jobsDBPartitionBuffer) StoreEachBatchRetry(ctx context.Context, jobBatches [][]*jobsdb.JobT) map[uuid.UUID]string {
	b.flushMu.RLock()
	defer b.flushMu.RUnlock()
	bufferedPartitions := b.getBufferedPartitionsSnapshot()
	if len(bufferedPartitions) == 0 {
		return b.primaryWriteJobsDB.StoreEachBatchRetry(ctx, jobBatches)
	}

	b.primaryWriteJobsDB.WithStoreSafeTx(ctx, func(tx jobsdb.StoreSafeTx) error {
		return b.withLatestBufferedPartitions(ctx, bufferedPartitions, tx.Tx(), func(bufferedPartitions map[string]struct{}) error {
			primaryJobs, bufferedJobs := b.spitJobs(lo.Flatten(jobBatches), bufferedPartitions)
			m := make(map[uuid.UUID]string)
			if len(primaryJobs) > 0 {
				var err error
				m, err = b.primaryWriteJobsDB.StoreEachBatchRetryInTx(ctx, tx, [][]*jobsdb.JobT{primaryJobs})
				if err != nil {
					return err
				}
			}
			if len(bufferedJobs) > 0 {
				return b.bufferWriteJobsDB.WithStoreSafeTxFromTx(ctx, tx.Tx(), func(tx jobsdb.StoreSafeTx) error {
					m1, err := b.primaryWriteJobsDB.StoreEachBatchRetryInTx(ctx, tx, [][]*jobsdb.JobT{primaryJobs})
					if err != nil {
						return err
					}
					maps.Copy(m, m1)
					return nil
				})
			}
			return nil
		})

	})
	return nil
}

func (b *jobsDBPartitionBuffer) StoreEachBatchRetryInTx(ctx context.Context, tx jobsdb.StoreSafeTx, jobBatches [][]*jobsdb.JobT) (map[uuid.UUID]string, error) {
	b.flushMu.RLock()
	defer b.flushMu.RUnlock()
	bufferedPartitions := b.getBufferedPartitionsSnapshot()
	if len(bufferedPartitions) == 0 {
		return b.primaryWriteJobsDB.StoreEachBatchRetryInTx(ctx, tx, jobBatches)
	}
	m := make(map[uuid.UUID]string)
	if err := b.withLatestBufferedPartitions(ctx, bufferedPartitions, tx.Tx(), func(bufferedPartitions map[string]struct{}) error {
		primaryJobs, bufferedJobs := b.spitJobs(lo.Flatten(jobBatches), bufferedPartitions)
		if len(primaryJobs) > 0 {
			var err error
			m, err = b.primaryWriteJobsDB.StoreEachBatchRetryInTx(ctx, tx, [][]*jobsdb.JobT{primaryJobs})
			if err != nil {
				return err
			}
		}
		if len(bufferedJobs) > 0 {
			if err := b.bufferWriteJobsDB.WithStoreSafeTxFromTx(ctx, tx.Tx(), func(tx jobsdb.StoreSafeTx) error {
				m1, err := b.primaryWriteJobsDB.StoreEachBatchRetryInTx(ctx, tx, [][]*jobsdb.JobT{primaryJobs})
				if err != nil {
					return err
				}
				maps.Copy(m, m1)
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return m, nil
}

func (b *jobsDBPartitionBuffer) spitJobs(jobList []*jobsdb.JobT, bufferedPartitions map[string]struct{}) (primary []*jobsdb.JobT, buffered []*jobsdb.JobT) {

	primary = []*jobsdb.JobT{}
	buffered = []*jobsdb.JobT{}
	for _, job := range jobList {
		if _, ok := bufferedPartitions[job.PartitionID]; ok {
			buffered = append(buffered, job)
		} else {
			primary = append(primary, job)
		}
	}
	return primary, buffered
}

func (b *jobsDBPartitionBuffer) withLatestBufferedPartitions(ctx context.Context, bufferedPartitionsSnapshot map[string]struct{}, tx *tx.Tx, f func(map[string]struct{}) error) error {
	if !b.refreshBufferedPartitionsOnEveryStore {
		return f(bufferedPartitionsSnapshot)
	}

	return jobsdb.WithDistributedSharedLock(ctx, tx, b.primaryWriteJobsDB.Identifier(), "refresh_buffered_partitions", func() error {
		// TODO: refresh buffered partitions
		b.getBufferedPartitionsSnapshot()
		return f(bufferedPartitionsSnapshot)
	})
}
