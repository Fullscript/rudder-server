CREATE TABLE IF NOT EXISTS buffered_partitions_versions (
  key TEXT PRIMARY KEY,
  version BIGINT NOT NULL DEFAULT 0
);

CREATE OR REPLACE FUNCTION bump_buffered_partitions_version()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  lock_name text := TG_ARGV[0];
  already_bumped boolean; -- to avoid multiple version bumps in the same transaction
BEGIN
  CREATE TEMP TABLE IF NOT EXISTS bumped_versions (
    key text PRIMARY KEY
  );
  SELECT EXISTS (SELECT 1 FROM bumped_versions WHERE key = lock_name) INTO already_bumped;
  IF NOT already_bumped THEN
  	PERFORM 1 FROM buffered_partitions_versions WHERE key = lock_name FOR UPDATE; -- lock the row
  	UPDATE buffered_partitions_versions SET version = version + 1 WHERE key = lock_name; -- bump version
  	INSERT INTO bumped_versions(key) VALUES (lock_name); -- mark as bumped
  END IF;
  RETURN NULL;
END;
$$;