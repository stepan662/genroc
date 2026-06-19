-- Buffered external-task signals (the push/webhook model). A signal addressed to an
-- (instance, external task) that arrives while the task is NOT currently armed is stored
-- here FIFO and consumed when the task next arms. It is kept in its own table (never in
-- context_data) so the single-writer-per-instance invariant holds: the signal endpoint
-- can append a row here while the engine owns the instance row, and the two serialize on
-- the instance row lock (DeliverSignal / ArmExternalOrConsumeSignal both take it).
CREATE TABLE process_signals (
    id          TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL,
    task_id     TEXT NOT NULL,
    payload     TEXT NOT NULL,
    created_at  BIGINT NOT NULL
);

-- Pop-oldest-for-(instance, task): the arm path deletes the row with the smallest
-- (created_at, id) for the instance+task, giving FIFO delivery per task.
CREATE INDEX idx_signals_fifo ON process_signals (instance_id, task_id, created_at, id);
