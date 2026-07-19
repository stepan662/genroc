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
func (e *Engine) handleCallError(inst *model.ProcessInstance, task *model.Task, errMsg, errCode string) advanceOutcome {
	// If the process is being cancelled, suppress retries and honour the cancellation
	// unless retries are exhausted / not configured — in that case error takes precedence.
	if inst.Status == model.StatusCancelling {
		matched := matchOnError(task, errCode)
		if matched != nil && inst.RetryCount < matched.Retries && isRetryAllowed(task, errCode, matched) {
			// Retries remain but we're cancelling — skip the retry and cancel cleanly.
			e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventCancelSkipRetry, Task: task.ID, Msg: errMsg, Code: errCode})
			return e.cancelInstance(inst)
		}
		// No retries available — error takes precedence over cancellation.
		return e.failInstance(inst, fmt.Sprintf("task %q: %s: %s", task.ID, errCode, errMsg))
	}

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

// cancelInstance moves the instance to its cancelled state and returns the terminal
// outcome (persisted by runAdvance via saveAndNotify).
func (e *Engine) cancelInstance(inst *model.ProcessInstance) advanceOutcome {
	inst.Status = model.StatusCancelled
	inst.WaitState = model.WaitStateNone
	// A retry-backoff parks with RetryCount > 0; clear its timer so a later retry
	// runs immediately. A delay parks with RetryCount == 0; keep wake_at so the
	// retry resumes toward the delay's original deadline.
	if inst.RetryCount > 0 {
		inst.WakeAt = nil
	}
	e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventCancelled})
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
