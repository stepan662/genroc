-- Replace the cancellation states with the pause/resume model.
--
-- 'cancelled' was a terminal state revived by RetryProcess, which conflated two
-- different operations: retrying a failed process (granting attempts its definition
-- did not authorise) and un-suspending one an operator had stopped. They are now
-- separate: 'paused' means only "does not continue automatically" — wait_state,
-- wake_at, retry_count and context are preserved verbatim, so resume is a status
-- flip rather than a revival. 'pausing' is the draining state of a leased instance
-- that will land in 'paused' once its in-flight task finishes.
UPDATE process_instances SET status = 'paused'  WHERE status = 'cancelled';
UPDATE process_instances SET status = 'pausing' WHERE status = 'cancelling';

-- Rebuild the partial runnable index (migration 010) over the renamed draining
-- state. 'pausing' stays in the runnable set purely for crash recovery: a worker
-- that dies holding a pausing row leaves it leased-but-dead, and only a reclaim can
-- settle it. 'paused' is deliberately absent — paused rows leave the index entirely,
-- so they are neither scanned by ClaimInstances nor maintained on write.
DROP INDEX IF EXISTS idx_instances_runnable;
CREATE INDEX idx_instances_runnable ON process_instances (created_at)
    WHERE status IN ('running', 'failing', 'pausing') AND wait_state <> 'waiting';
