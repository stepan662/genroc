CREATE TABLE IF NOT EXISTS process_definitions (
    name       TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    definition TEXT    NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (name, version)
);

CREATE TABLE IF NOT EXISTS process_instances (
    id               TEXT        NOT NULL PRIMARY KEY,
    process_name     TEXT        NOT NULL,
    process_version  INTEGER     NOT NULL,
    step_queue       TEXT        NOT NULL DEFAULT '[]',
    context_data     TEXT        NOT NULL DEFAULT '{}',
    retry_count      INTEGER     NOT NULL DEFAULT 0,
    next_retry_at    TIMESTAMPTZ,
    status           TEXT        NOT NULL DEFAULT 'running',
    error            TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL,
    worker_id        TEXT,
    lease_expires_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_instances_pending
    ON process_instances (status, next_retry_at);
