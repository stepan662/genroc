package engine

import (
	"fmt"
	"strings"

	"genroc/internal/db"
	"genroc/internal/model"
	"genroc/internal/transport"
)

// isRetryAllowed reports whether a retry is safe for the given task and error.
// For idempotent tasks (the default) retries are always governed by on_error rules.
// For non-idempotent tasks, a retry is only allowed when we know the remote call
// never started: start.* error codes, or an on_error rule with executed:false.
func isRetryAllowed(task *model.Task, errCode string, matched *model.ErrorCase) bool {
	if task.OnlyOnce == nil || !*task.OnlyOnce {
		return true
	}
	if matched != nil && matched.NotReached != nil && *matched.NotReached {
		return true
	}
	return strings.HasPrefix(errCode, "pre.")
}

// matchOnError returns the first ErrorCase whose Code patterns match errCode,
// or whose Code list is empty (catch-all). Returns nil when no rule matches.
func matchOnError(task *model.Task, errCode string) *model.ErrorCase {
	for i := range task.OnError {
		c := &task.OnError[i]
		if len(c.Code) == 0 {
			return c
		}
		for _, pat := range c.Code {
			if transport.SQLLikeMatch(pat, errCode) {
				return c
			}
		}
	}
	return nil
}

// handleCallError evaluates on_error rules, retries if allowed, injects $error
// context, and routes to the matching goto or fails the instance. It returns the
// outcome to persist (runAdvance writes it).
// A pause needs no special case here. The on_error rules run exactly as they would
// otherwise: a retry is scheduled with its normal backoff, and the write that persists
// it lands the pending pause (the CASE in UpdateInstance), so the instance settles into
// 'paused' still holding the attempt the definition granted it. Resuming continues that
// schedule with nothing spent and nothing skipped — which is the whole difference
// between resuming a pause and retrying a failure.
func (e *Engine) handleCallError(inst *model.ProcessInstance, task *model.Task, errMsg, errCode string) advanceOutcome {
	matched := matchOnError(task, errCode)

	if matched != nil && inst.RetryCount < matched.Retries && isRetryAllowed(task, errCode, matched) {
		inst.RetryCount++
		next := db.Now().Add(e.retryDelay(inst.RetryCount))
		inst.WakeAt = &next
		retryMsg := fmt.Sprintf("%s (attempt %d/%d)", errMsg, inst.RetryCount, matched.Retries)
		e.audit(inst, logEvent{Level: model.LogWarn, Event: model.EventRetryScheduled, Task: task.ID, Msg: retryMsg, Code: errCode})
		return advanceOutcome{kind: outcomeUpdate}
	}

	inst.ContextData["error"] = map[string]any{
		"task":    task.ID,
		"message": errMsg,
		"code":    errCode,
	}

	if matched != nil && matched.Goto != "" {
		if matched.Goto == model.GotoEnd {
			inst.Status = model.StatusCompleted
			inst.WakeAt = nil
			e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventErrorCompleted, Task: task.ID, Msg: errMsg, Code: errCode})
			return advanceOutcome{kind: outcomeTerminal}
		}
		if err := e.resolveGoto(inst, matched.Goto); err != nil {
			return e.failInstance(inst, err.Error())
		}
		inst.Task = matched.Goto
		inst.RetryCount = 0
		inst.WakeAt = nil
		e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventErrorRoute, Task: task.ID, Msg: errMsg + " → " + matched.Goto, Code: errCode})
		return advanceOutcome{kind: outcomeUpdate}
	}

	return e.failInstance(inst, fmt.Sprintf("task %q: %s: %s", task.ID, errCode, errMsg))
}

// failInstance moves the instance to its failed state and returns the terminal
// outcome (persisted by runAdvance via saveAndNotify).
func (e *Engine) failInstance(inst *model.ProcessInstance, reason string) advanceOutcome {
	inst.Status = model.StatusFailed
	inst.WaitState = model.WaitStateNone
	inst.Error = reason
	inst.WakeAt = nil
	e.audit(inst, logEvent{Level: model.LogError, Event: model.EventInstanceFailed, Msg: reason})
	return advanceOutcome{kind: outcomeTerminal}
}

// settlePausing lands a 'pausing' instance in 'paused' without touching anything else:
// wait_state, wake_at, retry_count and context all carry over untouched, so resuming is
// a status flip and the instance picks up exactly where it stopped. Timers keep running
// while paused, so a delay or backoff that elapses meanwhile is simply due on resume.
//
// This path is reached only when a worker died holding the instance — in the normal case
// the pause lands in SQL when the owning worker writes its finished task. That makes the
// reclaim check below load-bearing rather than defensive: the interrupted task may have
// already executed on the dead worker, and pausing here would launder that into a silent
// re-execution when the operator resumes.
func (e *Engine) settlePausing(inst *model.ProcessInstance, task *model.Task) advanceOutcome {
	if inst.ReclaimedExpired && task != nil && task.Action != nil && task.OnlyOnce != nil && *task.OnlyOnce {
		return e.failInstance(inst, fmt.Sprintf(
			"task %q is only_once and was interrupted by a lease takeover; cannot re-execute", task.ID))
	}
	inst.Status = model.StatusPaused
	// The other half of inst_paused: PauseProcess logs the rows it settled directly, and
	// this covers the leased one it could only mark 'pausing'. Same event and level, so
	// the trail reads uniformly — every instance that becomes paused says so once.
	e.audit(inst, logEvent{Level: model.LogDebug, Event: model.EventPaused, Task: inst.Task,
		Msg: "in-flight task settled; instance paused"})
	return advanceOutcome{kind: outcomeTerminal}
}

// settleFailing finalises a draining 'failing' instance once its children have
// settled (it only becomes claimable then). The error was already recorded when
// the failure propagated up; saveAndNotify (via the terminal outcome) cascades the
// settlement one level up.
func (e *Engine) settleFailing(inst *model.ProcessInstance) advanceOutcome {
	inst.Status = model.StatusFailed
	inst.WaitState = model.WaitStateNone
	inst.WakeAt = nil
	e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventInstanceSettled, Msg: inst.Error})
	return advanceOutcome{kind: outcomeTerminal}
}
