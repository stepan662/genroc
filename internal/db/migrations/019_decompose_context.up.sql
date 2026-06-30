-- Decompose the single context_data JSON blob into typed columns. Each column owns
-- one concern with its own write cadence, so a tick no longer re-marshals the whole
-- of an instance's state, and the value-bearing slots (input_data, outputs_data,
-- output_data) carry envelopes whose large values live out-of-line in process_objects.
--
--   input_data    — the process input envelope (write-once at creation)
--   outputs_data  — {order:[taskID...], items:{taskID: envelope}} (grows as tasks finish)
--   output_data   — the final process output envelope (written on completion)
--   error_data    — the {task,message,code} error map (plain JSON, small)
--   external_data — parked external-task bookkeeping {task_id,token,input,result} (plain JSON)
--   engine_state  — spawn/children bookkeeping {children,spawn_*} (plain JSON, small)
--
-- Empty string means the slot is absent. Prototype: context_data is dropped with no
-- backfill (existing rows are not migrated).
ALTER TABLE process_instances DROP COLUMN context_data;
ALTER TABLE process_instances ADD COLUMN input_data    TEXT NOT NULL DEFAULT '';
ALTER TABLE process_instances ADD COLUMN outputs_data  TEXT NOT NULL DEFAULT '';
ALTER TABLE process_instances ADD COLUMN output_data   TEXT NOT NULL DEFAULT '';
ALTER TABLE process_instances ADD COLUMN error_data    TEXT NOT NULL DEFAULT '';
ALTER TABLE process_instances ADD COLUMN external_data TEXT NOT NULL DEFAULT '';
ALTER TABLE process_instances ADD COLUMN engine_state  TEXT NOT NULL DEFAULT '';
