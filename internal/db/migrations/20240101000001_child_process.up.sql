ALTER TABLE process_instances ADD COLUMN parent_id  TEXT NOT NULL DEFAULT '';
ALTER TABLE process_instances ADD COLUMN call_stack TEXT NOT NULL DEFAULT '[]';

CREATE INDEX idx_instances_parent ON process_instances (parent_id)
    WHERE parent_id != '';
