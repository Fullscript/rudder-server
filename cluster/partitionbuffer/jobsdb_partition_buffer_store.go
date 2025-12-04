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
	if !b.canStore {
		return ErrStoreNotSupported
	}
	b.flushMu.RLock()
	defer b.flushMu.RUnlock()

	bufferedPartitions := b.getBufferedPartitionsSnapshot() // TODO: we need a lock
	if bufferedPartitions.Len() == 0 {
		return b.primaryWriteJobsDB.Store(ctx, jobList)
	}
	b.primaryWriteJobsDB.WithStoreSafeTx(ctx, func(tx jobsdb.StoreSafeTx) error {
		return b.withLatestBufferedPartitions(ctx, bufferedPartitions, tx.Tx(), func(bufferedPartitions *readOnlyMap[string, struct{}]) error {
			primaryJobs, bufferedJobs := spitJobs(jobList, bufferedPartitions)
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
	if !b.canStore {
		return ErrStoreNotSupported
	}
	b.flushMu.RLock()
	defer b.flushMu.RUnlock()
	bufferedPartitions := b.getBufferedPartitionsSnapshot()
	if bufferedPartitions.Len() == 0 {
		return b.primaryWriteJobsDB.StoreInTx(ctx, tx, jobList)
	}
	return b.withLatestBufferedPartitions(ctx, bufferedPartitions, tx.Tx(), func(bufferedPartitions *readOnlyMap[string, struct{}]) error {
		primaryJobs, bufferedJobs := spitJobs(jobList, bufferedPartitions)
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
	if !b.canStore {
		res := make(map[uuid.UUID]string)
		for _, batch := range jobBatches {
			for _, job := range batch {
				res[job.UUID] = ErrStoreNotSupported.Error()
			}
		}
		return res
	}
	b.flushMu.RLock()
	defer b.flushMu.RUnlock()
	bufferedPartitions := b.getBufferedPartitionsSnapshot()
	if bufferedPartitions.Len() == 0 {
		return b.primaryWriteJobsDB.StoreEachBatchRetry(ctx, jobBatches)
	}

	b.primaryWriteJobsDB.WithStoreSafeTx(ctx, func(tx jobsdb.StoreSafeTx) error {
		return b.withLatestBufferedPartitions(ctx, bufferedPartitions, tx.Tx(), func(bufferedPartitions *readOnlyMap[string, struct{}]) error {
			primaryJobs, bufferedJobs := spitJobs(lo.Flatten(jobBatches), bufferedPartitions)
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
	if !b.canStore {
		res := make(map[uuid.UUID]string)
		for _, batch := range jobBatches {
			for _, job := range batch {
				res[job.UUID] = ErrStoreNotSupported.Error()
			}
		}
		return res, nil
	}
	b.flushMu.RLock()
	defer b.flushMu.RUnlock()
	bufferedPartitions := b.getBufferedPartitionsSnapshot()
	if bufferedPartitions.Len() == 0 {
		return b.primaryWriteJobsDB.StoreEachBatchRetryInTx(ctx, tx, jobBatches)
	}
	m := make(map[uuid.UUID]string)
	if err := b.withLatestBufferedPartitions(ctx, bufferedPartitions, tx.Tx(), func(bufferedPartitions *readOnlyMap[string, struct{}]) error {
		primaryJobs, bufferedJobs := spitJobs(lo.Flatten(jobBatches), bufferedPartitions)
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

// spitJobs splits the jobList into two lists: one for primary partitions and one for buffered partitions
func spitJobs(jobList []*jobsdb.JobT, bufferedPartitions *readOnlyMap[string, struct{}]) (primary []*jobsdb.JobT, buffered []*jobsdb.JobT) {

	primary = []*jobsdb.JobT{}
	buffered = []*jobsdb.JobT{}
	for _, job := range jobList {
		if bufferedPartitions.Has(job.PartitionID) {
			buffered = append(buffered, job)
		} else {
			primary = append(primary, job)
		}
	}
	return primary, buffered
}

// withLatestBufferedPartitions refreshes the buffered partitions from the database if needed and then calls the provided function with the latest snapshot
func (b *jobsDBPartitionBuffer) withLatestBufferedPartitions(ctx context.Context, bufferedPartitions *readOnlyMap[string, struct{}], tx *tx.Tx, f func(*readOnlyMap[string, struct{}]) error) error {
	if !b.refreshBufferedPartitionsOnEveryStore {
		return f(bufferedPartitions)
	}
	diff, err := b.versionDiff(ctx, tx) // get the version difference along with a read lock
	if err != nil {
		return err
	}
	if diff == 0 {
		return f(bufferedPartitions)
	}
	bufferedPartitions, err = b.getBufferedPartitionsInTx(ctx, tx)
	if err != nil {
		return err
	}
	b.bufferedPartitionsMu.Lock()
	b.bufferedPartitionsVersion += diff
	b.bufferedPartitions = bufferedPartitions
	b.bufferedPartitionsMu.Unlock()
	return f(bufferedPartitions)
}

// versionDiff returns the difference between the version of buffered partitions in the database and the current version in memory
func (b *jobsDBPartitionBuffer) versionDiff(ctx context.Context, tx *tx.Tx) (int, error) {
	dbVersion, err := b.getBufferedPartitionsVersionInTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	b.bufferedPartitionsMu.RLock()
	defer b.bufferedPartitionsMu.RUnlock()
	return dbVersion - b.bufferedPartitionsVersion, nil
}
