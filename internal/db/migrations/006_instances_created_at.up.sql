CREATE INDEX IF NOT EXISTS idx_instances_status_created_at
    ON process_instances (status, created_at);
