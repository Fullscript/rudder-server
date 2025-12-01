package buffer

import "github.com/rudderlabs/rudder-server/jobsdb"

type Opt func(*jobsDBPartitionBuffer)

// WithReadWriteJobsDBs sets both read and write JobsDBs for primary and buffer
func WithReadWriteJobsDBs(primary jobsdb.JobsDB, buffer jobsdb.JobsDB) Opt {
	return func(b *jobsDBPartitionBuffer) {
		b.JobsDB = primary
		b.primaryReadJobsDB = primary
		b.primaryWriteJobsDB = primary
		b.bufferReadJobsDB = buffer
		b.bufferWriteJobsDB = buffer
		b.lifecycleJobsDBs = []jobsdb.JobsDB{primary, buffer}
	}
}

// WithWithWriterOnlyJobsDBs sets only the writer JobsDBs for primary and buffer
func WithWithWriterOnlyJobsDBs(primaryWriter jobsdb.JobsDB, bufferWriter jobsdb.JobsDB) Opt {
	return func(b *jobsDBPartitionBuffer) {
		b.JobsDB = primaryWriter
		b.primaryWriteJobsDB = primaryWriter
		b.bufferWriteJobsDB = bufferWriter
		b.lifecycleJobsDBs = []jobsdb.JobsDB{primaryWriter, bufferWriter}
	}
}

// WithReaderOnlyAndFlushJobsDBs sets only the reader JobsDBs for primary and buffer, and writer as primary
func WithReaderOnlyAndFlushJobsDBs(primaryReader jobsdb.JobsDB, bufferReader jobsdb.JobsDB, primaryWriter jobsdb.JobsDB) Opt {
	return func(b *jobsDBPartitionBuffer) {
		b.JobsDB = primaryWriter
		b.primaryReadJobsDB = primaryReader
		b.bufferReadJobsDB = bufferReader
		b.primaryWriteJobsDB = primaryWriter
		b.lifecycleJobsDBs = []jobsdb.JobsDB{primaryReader, bufferReader, primaryWriter}
	}
}

func NewJobsDBPartitionBuffer(opts ...Opt) JobsDBPartitionBuffer {
	jb := &jobsDBPartitionBuffer{}
	for _, opt := range opts {
		opt(jb)
	}
	return jb
}
