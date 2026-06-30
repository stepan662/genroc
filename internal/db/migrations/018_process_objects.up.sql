-- process_objects: an out-of-line store for values too large to keep inline on a
-- hot row (task outputs, process input/output) and for large log payloads. Rows are
-- immutable and write-once, addressed by the sha256 hash of their content, and owned
-- by the instance that produced them (instance_id).
--
-- An object stays alive while it is referenced by EITHER consumer, tracked
-- independently so neither can pull it out from under the other:
--   pinned    = 1 while a live context value-slot (input/output) references it.
--   log_until = unix-ms horizon a log row needs it until (NULL = no log references it).
--
-- The two can both be set: when a value has no secrets its context object and its
-- (pre-redacted) log object are byte-identical, hash to the same row, and so are
-- shared. A row is collectable only once it is neither pinned nor still needed by a
-- log: pinned = 0 AND (log_until IS NULL OR log_until < now). A context dereference
-- (a slot replaced by another value) deletes the row immediately when no log needs
-- it, so a replaced value — and any secret in it — does not linger; otherwise it is
-- just unpinned and the GC sweep reclaims it once the log horizon passes.
--
-- Serving safety: only log-referenced rows (log_until IS NOT NULL) are served raw via
-- the log endpoint. Their content is always safe — a log only ever references content
-- it wrote pre-redacted, and a shared (collided) row is byte-identical to that, hence
-- secret-free. Unredacted context-only objects (log_until NULL) are never served.
CREATE TABLE process_objects (
    instance_id TEXT    NOT NULL,
    hash        TEXT    NOT NULL,   -- sha256 hex of content
    content     TEXT    NOT NULL,   -- JSON payload (context: unredacted; log: pre-redacted)
    size        BIGINT  NOT NULL,   -- byte length of content
    pinned      INTEGER NOT NULL DEFAULT 0, -- 1 = referenced by a live context slot
    log_until   BIGINT,             -- unix-ms a log needs it until; NULL = no log reference
    created_at  BIGINT  NOT NULL,
    PRIMARY KEY (instance_id, hash)
);

-- The GC sweep scans the collectable set (pinned = 0, ordered by log horizon).
CREATE INDEX idx_objects_gc ON process_objects (pinned, log_until);
