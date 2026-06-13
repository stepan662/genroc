-- Supports chunked lease renewal: the renewer selects this worker's instances
-- ordered by lease_expires_at (soonest-to-expire first) in small batches. Partial
-- on worker_id IS NOT NULL so the index only covers currently-leased rows (few),
-- not the whole history. Composite with lease_expires_at so the filter, ordering,
-- and limit are all served by one index range scan.
CREATE INDEX IF NOT EXISTS idx_instances_worker
    ON process_instances (worker_id, lease_expires_at)
    WHERE worker_id IS NOT NULL;
