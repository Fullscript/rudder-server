CREATE TABLE IF NOT EXISTS migrating_partitions_dedup (
  key TEXT PRIMARY KEY,
  last_job_id BIGINT NOT NULL DEFAULT 0
);