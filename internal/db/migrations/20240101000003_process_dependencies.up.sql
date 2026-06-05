CREATE TABLE process_dependencies (
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
CREATE INDEX process_dependencies_child ON process_dependencies (child_name, child_version);
