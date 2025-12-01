package buffer

import (
	"context"
	"maps"
	"sync"

	"github.com/rudderlabs/rudder-server/jobsdb"
)

type JobsDBPartitionBuffer interface {
	jobsdb.JobsDB
	// BufferPartitions marks the provided partitions to be buffered
	BufferPartitions(ctx context.Context, partitions []string) error
	// RefreshBufferedPartitions refreshes the list of buffered partitions from the database
	RefreshBufferedPartitions(ctx context.Context, partitions []string) error
	// FlushBufferedPartitions flushes the buffered data for the provided partitions to the database and unmarks them as buffered.
	FlushBufferedPartitions(ctx context.Context, partitions []string) error
}

type jobsDBPartitionBuffer struct {
	jobsdb.JobsDB
	refreshBufferedPartitionsOnEveryStore bool
	primaryWriteJobsDB                    jobsdb.JobsDB
	primaryReadJobsDB                     jobsdb.JobsDB

	bufferWriteJobsDB jobsdb.JobsDB
	bufferReadJobsDB  jobsdb.JobsDB

	lifecycleJobsDBs []jobsdb.JobsDB

	flushMu              sync.RWMutex
	bufferedPartitionsMu sync.RWMutex
	bufferedPartitions   map[string]struct{}
}

func (b *jobsDBPartitionBuffer) BufferPartitions(ctx context.Context, partitions []string) error {
	return nil
}

func (b *jobsDBPartitionBuffer) RefreshBufferedPartitions(ctx context.Context, partitions []string) error {
	return nil
}

func (b *jobsDBPartitionBuffer) FlushBufferedPartitions(ctx context.Context, partitions []string) error {
	return nil
}

func (b *jobsDBPartitionBuffer) getBufferedPartitionsSnapshot() map[string]struct{} {
	b.bufferedPartitionsMu.RLock()
	defer b.bufferedPartitionsMu.RUnlock()
	return maps.Clone(b.bufferedPartitions)
}
