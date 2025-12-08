package partitionbuffer

import (
	"context"
	"fmt"
	"time"

	"github.com/rudderlabs/rudder-go-kit/logger"
	"github.com/rudderlabs/rudder-server/jobsdb"
	"github.com/rudderlabs/rudder-server/utils/tx"
	"github.com/samber/lo"
)

// FlushBufferedPartitions flushes the buffered data for the provided partition ids to the database and unmarks them as buffered.
func (b *jobsDBPartitionBuffer) FlushBufferedPartitions(ctx context.Context, partitions []string) error {
	start := time.Now()
	moveTimeout := time.After(b.flushMoveTimeout.Load())

	if !b.canFlush {
		return ErrFlushNotSupported
	}
	// move in batches until we stop reaching limits
	for limitsReached := true; limitsReached; {
		var err error
		select {
		case <-moveTimeout:
			// timeout reached, break out to switchover
			b.logger.Warnn("flush move timeout reached, proceeding to switchover",
				logger.NewStringField("partitions", fmt.Sprintf("%v", partitions)),
				logger.NewDurationField("duration", time.Since(start)),
			)
			limitsReached = false
		default:
			limitsReached, err = b.moveBufferedPartitions(ctx, partitions, b.flushBatchSize.Load(), b.flushPayloadSize.Load())
			if err != nil {
				return fmt.Errorf("moving buffered partitions: %w", err)
			}
		}
	}
	// switchover
	if err := b.switchoverBufferedPartitions(ctx, partitions, b.flushBatchSize.Load(), b.flushPayloadSize.Load()); err != nil {
		return fmt.Errorf("switchover of buffered partitions: %w", err)
	}
	return nil
}

// moveBufferedPartitions moves a batch of buffered jobs to the primary JobsDB for the given partition IDs. It returns whether any limits were reached during the fetch.
// If limits were reached, the caller should call this method again to move more data.
func (b *jobsDBPartitionBuffer) moveBufferedPartitions(ctx context.Context, partitionIDs []string, batchSize int, payloadSize int64) (limitsReached bool, err error) {
	bufferedJobs, err := b.bufferReadJobsDB.GetUnprocessed(ctx, jobsdb.GetQueryParams{
		PartitionFilters: partitionIDs,
		JobsLimit:        batchSize,
		PayloadSizeLimit: int64(payloadSize),
	})
	if err != nil {
		return false, err
	}
	if len(bufferedJobs.Jobs) > 0 {
		now := time.Now()
		statusList := lo.Map(bufferedJobs.Jobs, func(job *jobsdb.JobT, _ int) *jobsdb.JobStatusT {
			return &jobsdb.JobStatusT{
				JobID:         job.JobID,
				JobState:      jobsdb.Succeeded.State,
				AttemptNum:    1,
				ExecTime:      now,
				RetryTime:     now,
				ErrorCode:     "200",
				ErrorResponse: []byte("{}"),
				Parameters:    []byte("{}"),
				JobParameters: job.Parameters,
				WorkspaceId:   job.WorkspaceId,
				PartitionID:   job.PartitionID,
			}
		})
		if err := b.primaryWriteJobsDB.WithStoreSafeTx(ctx, func(tx jobsdb.StoreSafeTx) error {
			if err := b.primaryWriteJobsDB.StoreInTx(ctx, tx, bufferedJobs.Jobs); err != nil {
				return fmt.Errorf("moving buffered jobs to primary jobsdb: %w", err)
			}
			// create job statuses
			if err := b.bufferReadJobsDB.WithUpdateSafeTxFromTx(ctx, tx.Tx(), func(tx jobsdb.UpdateSafeTx) error {
				return b.bufferReadJobsDB.UpdateJobStatusInTx(ctx, tx, statusList,
					[]string{"flush"}, // intentionally using one random customValFilter "flush" so that no jobs cache will not invalidate a full branch
					nil)
			}); err != nil {
				return fmt.Errorf("updating job statuses for moved jobs: %w", err)
			}
			return nil
		}); err != nil {
			return false, err
		}
	}
	return bufferedJobs.DSLimitsReached || bufferedJobs.LimitsReached, nil
}

func (b *jobsDBPartitionBuffer) switchoverBufferedPartitions(ctx context.Context, partitionIDs []string, batchSize int, payloadSize int64) (err error) {
	b.bufferedPartitionsMu.Lock()
	defer b.bufferedPartitionsMu.Unlock()
	return b.WithTx(func(tx *tx.Tx) error {
		if b.differentReaderWriterDBs {
			// disable idle_in_transaction_session_timeout for the duration of this transaction, since it may take long to move all remaining data
			tx.ExecContext(ctx, "SET LOCAL idle_in_transaction_session_timeout = '0ms'")
			// mark partitions as unbuffered in the database early, for holding the global lock
			if err := b.removeBufferPartitions(ctx, tx, partitionIDs); err != nil {
				return fmt.Errorf("removing buffered partitions during switchover: %w", err)
			}
		}
		// move any remaining buffered data
		for limitsReached := true; limitsReached; {
			limitsReached, err = b.moveBufferedPartitions(ctx, partitionIDs, batchSize, int64(payloadSize))
			if err != nil {
				return fmt.Errorf("moving buffered partitions during switchover: %w", err)
			}
		}
		if !b.differentReaderWriterDBs {
			// mark partitions as unbuffered in the database late
			if err := b.removeBufferPartitions(ctx, tx, partitionIDs); err != nil {
				return fmt.Errorf("removing buffered partitions during switchover: %w", err)
			}
		}
		return nil
	})
}
