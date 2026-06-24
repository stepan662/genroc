-- Index backing the default instance listing (ListInstances), which orders by most
-- recently active first: (updated_at, id) with the UUIDv7 PK as tiebreaker. Unlike
-- idx_external_queue this is a full (non-partial) index because the list spans all
-- statuses. process_instances is high-churn, so this adds one index to maintain per
-- write; that cost is acceptable for the primary monitoring view, and the Postgres
-- aggressive-autovacuum bootstrap already keeps the table's dead tuples in check.
-- Both engines use it.
CREATE INDEX idx_instances_updated_at ON process_instances (updated_at, id);
