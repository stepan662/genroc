CREATE TABLE process_channels (
    name       TEXT        NOT NULL,
    channel    TEXT        NOT NULL,
    version    INTEGER     NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (name, channel),
    FOREIGN KEY (name, version) REFERENCES process_definitions(name, version)
);
