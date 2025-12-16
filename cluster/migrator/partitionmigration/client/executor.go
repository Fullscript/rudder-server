package client

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/rudderlabs/rudder-server/jobsdb"
	proto "github.com/rudderlabs/rudder-server/proto/cluster"
	"github.com/samber/lo"
	"golang.org/x/sync/errgroup"
)

func MigratePartitions(ctx context.Context, migrationJobID string, partitionIDs []string, sourceDB jobsdb.JobsDB, targetClient PartitionMigratorClient) error {
	stream, err := targetClient.StreamJobs(ctx)
	if err != nil {
		return fmt.Errorf("initiating stream jobs: %w", err)
	}
	defer func() { _ = stream.CloseSend() }()
	batchSize := 10000
	chunkSize := 1000
	g, cancel := errgroup.WithContext(ctx)
	defer cancel()

	var bachedMu sync.Mutex
	batches := map[int][]*jobsdb.JobStatusT{}
	// receiver goroutine
	g.Go(func() error {
		ack, err := stream.Recv()
		if err == io.EOF { // stream ended
			return nil
		}
		if err != nil {
			return fmt.Errorf("receiving ack: %w", err)
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				batchID := int(ack.BatchIndex)
				bachedMu.Lock()
				statusList, exists := batches[batchID]
				delete(batches, batchID)
				bachedMu.Unlock()
				if exists {
					// need to provide custom val filter for not invalidating cache branch
					if err := sourceDB.UpdateJobStatus(ctx, statusList, []string{"migrated"}, nil); err != nil {
						return fmt.Errorf("updating job status for batch %d: %w", batchID, err)
					}
				}
			}
		}
	})
	g.Go(func() error {
		defer func() {
			_ = stream.CloseSend() // close the send direction of the stream on exit
		}()
		err = stream.Send(&proto.StreamJobsRequest{Payload: &proto.StreamJobsRequest_Metadata{
			Metadata: &proto.JobStreamMetadata{
				MigrationJobId: migrationJobID,
				TablePrefix:    sourceDB.Identifier(),
			},
		}})
		if err != nil {
			return fmt.Errorf("sending metadata: %w", err)
		}
		batchIndex := 0
		for {
			jobs, err := sourceDB.GetToProcess(ctx, jobsdb.GetQueryParams{
				PartitionFilters: partitionIDs,
				JobsLimit:        batchSize,
			}, nil)
			if err != nil {
				return fmt.Errorf("getting jobs to process: %w", err)
			}
			if len(jobs.Jobs) == 0 && !jobs.DSLimitsReached {
				// no more jobs to process
				return nil
			}
			chunks := lo.Chunk(jobs.Jobs, chunkSize)
			for i, chunk := range chunks {
				proto.
			}
		}
	})

}
