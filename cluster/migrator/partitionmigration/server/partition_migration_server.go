package server

import (
	"context"
	"fmt"
	"io"
	"sync"

	kitctx "github.com/rudderlabs/rudder-go-kit/context"
	"github.com/rudderlabs/rudder-go-kit/logger"
	"github.com/rudderlabs/rudder-go-kit/stats"
	"github.com/rudderlabs/rudder-server/jobsdb"
	proto "github.com/rudderlabs/rudder-server/proto/cluster"
	"github.com/rudderlabs/rudder-server/utils/tx"
	"github.com/samber/lo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Opt func(*partitionMigrationServer)

// WithLogger sets the logger for the PartitionMigrationServer.
func WithLogger(logger logger.Logger) Opt {
	return func(s *partitionMigrationServer) {
		s.logger = logger
	}
}

// WithStats sets the stats for the PartitionMigrationServer.
func WithStats(stats stats.Stats) Opt {
	return func(s *partitionMigrationServer) {
		s.stats = stats
	}
}

// WithDedupEnabled sets whether deduplication is enabled for the PartitionMigrationServer (enabled by default).
func WithDedupEnabled(dedupEnabled bool) Opt {
	return func(s *partitionMigrationServer) {
		s.dedupEnabled = dedupEnabled
	}
}

// NewPartitionMigrationServer creates a new PartitionMigrationServer instance.
func NewPartitionMigrationServer(ctx context.Context, jobsdbs map[string]jobsdb.JobsDB, opts ...Opt) proto.PartitionMigratorServer {
	s := &partitionMigrationServer{
		lifecycleCtx:     ctx,
		activeMigrations: make(map[string]struct{}),
		jobsdbs:          jobsdbs,
		dedupEnabled:     true,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.logger == nil {
		s.logger = logger.NewLogger().Child("partition-migration-server")
	}
	if s.stats == nil {
		s.stats = stats.Default
	}
	return s
}

type partitionMigrationServer struct {
	proto.UnimplementedPartitionMigratorServer
	lifecycleCtx context.Context // TODO: do we need this?
	logger       logger.Logger
	stats        stats.Stats
	jobsdbs      map[string]jobsdb.JobsDB // map of table prefix to jobsdb instance
	dedupEnabled bool                     // whether deduplication is enabled when receiving jobs

	activeMigrationsMu sync.Mutex          // protects activeMigrations
	activeMigrations   map[string]struct{} // set of active migration keys (migrationJobID_tablePrefix)
}

func (s *partitionMigrationServer) StreamJobs(stream proto.PartitionMigrator_StreamJobsServer) error {
	// merge the stream context with the server lifecycle context
	ctx, cancel := kitctx.MergedContext(stream.Context(), s.lifecycleCtx)
	defer cancel()

	// receive the first message containing metadata
	r, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receiving metadata: %w", err)
	}
	metadata := r.GetMetadata()
	if metadata == nil {
		return status.New(codes.FailedPrecondition, "first message must be metadata").Err()
	}

	// resolve the jobsdb for the given table prefix
	db := s.jobsdbs[metadata.TablePrefix]
	if db == nil {
		return status.New(codes.FailedPrecondition, "no jobsdb found for the given table prefix").Err()
	}

	// construct the migration key, which is a combination of migration job ID and table prefix
	migrationKey := metadata.MigrationJobId + "_" + metadata.TablePrefix

	// only allow one active migration per migration key
	s.activeMigrationsMu.Lock()
	if _, exists := s.activeMigrations[migrationKey]; exists {
		s.activeMigrationsMu.Unlock()
		return status.New(codes.AlreadyExists, "a migration for the same migration job id and table prefix is already in progress").Err()
	}
	s.activeMigrations[migrationKey] = struct{}{}
	s.activeMigrationsMu.Unlock()
	defer func() { // clean up active migration entry
		s.activeMigrationsMu.Lock()
		delete(s.activeMigrations, migrationKey)
		s.activeMigrationsMu.Unlock()
	}()

	dedupActive := s.dedupEnabled // flag to indicate if deduplication is still active
	var lastJobID int64
	if s.dedupEnabled {
		// Load lastJobID from persistent storage to resume interrupted migrations
		if err := db.WithTx(func(tx *tx.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT last_job_id FROM migrating_partitions_dedup WHERE key = $1), 0)", migrationKey).Scan(&lastJobID)
		}); err != nil {
			return fmt.Errorf("loading lastJobID for migration %s: %w", migrationKey, err)
		}
	}

	batches := make(chan streamingBatch)
	// setup a goroutine for storing batches
	var storeErrMu sync.Mutex
	var storeErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		for batch := range batches {
			if len(batch.jobs) > 0 { // store only non-empty batches (we may have empty batches after deduplication)
				newLastJobID := batch.jobs[len(batch.jobs)-1].JobID // TODO: persist lastJobID for resuming interrupted migrations
				// store the batch
				if err := db.WithStoreSafeTx(ctx, func(tx jobsdb.StoreSafeTx) error {
					// store jobs
					if err := db.StoreInTx(ctx, tx, batch.jobs); err != nil {
						return fmt.Errorf("storing jobs in %s jobsdb: %w", metadata.TablePrefix, err)
					}
					if s.dedupEnabled {
						// update lastJobID for deduplication
						if _, err := tx.SqlTx().ExecContext(ctx,
							"INSERT INTO migrating_partitions_dedup (key, last_job_id) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET last_job_id = EXCLUDED.last_job_id;",
							migrationKey,
							newLastJobID,
						); err != nil {
							return fmt.Errorf("updating last_job_id for migration %s: %w", migrationKey, err)
						}
					}
					return nil
				}); err != nil {
					storeErrMu.Lock()
					storeErr = fmt.Errorf("storing jobs batch %d: %w", batch.index, err)
					storeErrMu.Unlock()
					cancel()
					return
				}
			}
			// acknowledge the batch
			if err := stream.Send(&proto.JobsBatchAck{BatchIndex: batch.index}); err != nil {
				storeErrMu.Lock()
				storeErr = fmt.Errorf("sending ack for jobs batch %d: %w", batch.index, err)
				storeErrMu.Unlock()
				cancel()
				return
			}
		}
	})

	// helper to stop the storing goroutine, wait for it to finish and return any errors
	stopStoreAndReturn := func(err error) error {
		var cause error
		storeErrMu.Lock()
		cause = storeErr
		storeErrMu.Unlock()
		if err != nil {
			cancel() // cancel the main context to abort ongoing operations
		}
		close(batches) // close the batches channel to stop the storing goroutine
		wg.Wait()      // wait for the storing goroutine to finish

		if cause != nil { // return the original error if any
			return cause
		}
		if err != nil {
			return err
		}
		return storeErr
	}

	// process incoming stream, collect jobs into batches and send them to the store goroutine
	var batch streamingBatch
	for {
		r, err := stream.Recv()
		if err == io.EOF { // client has finished sending
			return stopStoreAndReturn(nil)
		}
		if err != nil {
			return stopStoreAndReturn(fmt.Errorf("receiving from client stream: %w", err))
		}
		chunk := r.GetChunk()
		if chunk == nil {
			return stopStoreAndReturn(status.New(codes.FailedPrecondition, "received message without job batch").Err())
		}
		if len(batch.jobs) == 0 { // first chunk of a new batch
			batch.index = chunk.BatchIndex
		}
		jobs, err := chunk.GetJobsdbJobs()
		if err != nil {
			return stopStoreAndReturn(fmt.Errorf("converting to jobsdb jobs: %w", err))
		}
		// deduplicate jobs if needed, i.e. ignore jobs with JobID <= lastJobID
		if dedupActive {
			jobs = lo.Filter(jobs, func(j *jobsdb.JobT, _ int) bool {
				return j.JobID > lastJobID
			})
			dedupedJobsCount := len(chunk.Jobs) - len(jobs)
			if dedupedJobsCount > 0 {
				s.logger.Infon("Deduplicated jobs during partition migration",
					logger.NewStringField("migrationKey", migrationKey),
					logger.NewIntField("dedupCount", int64(dedupedJobsCount)),
				)
			}
			if len(jobs) > 0 {
				dedupActive = false // disable dedup after the first non-duplicate job
			}
		}
		batch.jobs = append(batch.jobs, jobs...)
		if chunk.LastChunk {
			select {
			case batches <- batch:
				// batch sent for storing
				batch = streamingBatch{}
			case <-ctx.Done(): // context cancelled
				return stopStoreAndReturn(fmt.Errorf("context done: %w", ctx.Err()))
			}
		}
	}
}

// streamingBatch represents a batch of jobs received from the client for migration
type streamingBatch struct {
	index int64          // index of the batch, used for acknowledgments
	jobs  []*jobsdb.JobT // jobs contained in the batch
}
