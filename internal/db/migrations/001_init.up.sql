CREATE TABLE IF NOT EXISTS process_definitions (
    name          TEXT    NOT NULL,
    version       INTEGER NOT NULL,
    definition    TEXT    NOT NULL,
    content_hash  TEXT    NOT NULL DEFAULT '',
    created_at    BIGINT  NOT NULL,
    PRIMARY KEY (name, version)
);
CREATE INDEX IF NOT EXISTS process_definitions_hash
    ON process_definitions (name, content_hash);

CREATE TABLE IF NOT EXISTS process_instances (
    id               TEXT    NOT NULL PRIMARY KEY,
    process_name     TEXT    NOT NULL,
    process_version  INTEGER NOT NULL,
    step_queue       TEXT    NOT NULL DEFAULT '[]',
    context_data     TEXT    NOT NULL DEFAULT '{}',
    parent_id        TEXT    NOT NULL DEFAULT '',
    call_stack       TEXT    NOT NULL DEFAULT '[]',
    retry_count      INTEGER NOT NULL DEFAULT 0,
    next_retry_at    BIGINT,
    status           TEXT    NOT NULL DEFAULT 'running',
    error            TEXT    NOT NULL DEFAULT '',
    created_at       BIGINT  NOT NULL,
    updated_at       BIGINT  NOT NULL,
    worker_id        TEXT,
    lease_expires_at BIGINT
);
CREATE INDEX IF NOT EXISTS idx_instances_pending
    ON process_instances (status, next_retry_at);
CREATE INDEX IF NOT EXISTS idx_instances_parent
    ON process_instances (parent_id)
    WHERE parent_id != '';

CREATE TABLE IF NOT EXISTS process_channels (
    name       TEXT    NOT NULL,
    channel    TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    updated_at BIGINT  NOT NULL,
    PRIMARY KEY (name, channel),
    FOREIGN KEY (name, version) REFERENCES process_definitions(name, version)
);

CREATE TABLE IF NOT EXISTS process_dependencies (
    parent_name    TEXT    NOT NULL,
    parent_version INTEGER NOT NULL,
    step_id        TEXT    NOT NULL,
    child_idx      INTEGER NOT NULL,
    child_name     TEXT    NOT NULL,
    child_version  INTEGER NOT NULL,
    PRIMARY KEY (parent_name, parent_version, step_id, child_idx),
    FOREIGN KEY (parent_name, parent_version) REFERENCES process_definitions(name, version),
    FOREIGN KEY (child_name, child_version)   REFERENCES process_definitions(name, version)
);
CREATE INDEX IF NOT EXISTS process_dependencies_child
    ON process_dependencies (child_name, child_version);
