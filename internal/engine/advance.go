package engine

import (
	"context"
	"fmt"
	"os"

	"genroc/internal/model"
)

// advanceOutcome is the next persisted state that advance() computes without
// writing it to the DB. runAdvance drops the in-flight marker first, then persist
// applies the outcome — so the lease-releasing write always happens after the
// marker is gone, in one place, and an instance is never simultaneously free in the
// DB and still marked in memory (which dispatch would misread as re-claiming live
// work). advance() is a pure state machine over the instance's own row; the one
// exception is a child spawn, which is a multi-row transaction that parks the parent
// (non-runnable, so the marker order is irrelevant) — it persists itself and returns
// outcomeNoop.
type advanceOutcome struct {
	kind outcomeKind
}

type outcomeKind uint8

const (
	outcomeProgress outcomeKind = iota // running checkpoint        → UpdateInstanceProgress
	outcomeUpdate                      // running, status/error set → UpdateInstance
	outcomeTerminal                    // completed/failed/cancelled → saveAndNotify
	outcomeNoop                        // advance already persisted (child spawn parked the parent)
)

// stop wraps an outcome as a non-nil pointer, the signal call helpers use to tell
// advance to stop the task loop and return this outcome (nil means "continue").
func stop(o advanceOutcome) *advanceOutcome { return &o }

// persist applies an advance outcome to the DB. It is the single place an in-flight
// advance releases its lease; runAdvance calls it after dropping the marker.
func (e *Engine) persist(inst *model.ProcessInstance, o advanceOutcome) error {
	switch o.kind {
	case outcomeTerminal:
		return e.saveAndNotify(inst)
	case outcomeProgress:
		return e.db.UpdateInstanceProgress(inst)
	case outcomeUpdate:
		return e.db.UpdateInstance(inst)
	case outcomeNoop:
		return nil
	default:
		return fmt.Errorf("unknown advance outcome %d", o.kind)
	}
}

// runAdvance advances one instance, then drops its in-flight marker before
// persisting the resulting state. Doing the delete before the store closes the
// window where the instance is free in the DB but still marked in memory. (For Tick,
// which keeps no marker, the delete is a harmless no-op.)
func (e *Engine) runAdvance(ctx context.Context, inst *model.ProcessInstance) error {
	outcome := e.advance(ctx, inst)
	e.inflight.Delete(inst.ID)
	if err := e.persist(inst, outcome); err != nil {
		return err
	}
	// A persisted advance may have produced immediately-runnable work: this instance
	// again (a running checkpoint), children spawned by a parked parent, or a parent
	// un-parked by this instance finishing. Nudge the pump to re-scan now rather than
	// idle until the next tick. A spurious nudge (nothing actually runnable) costs only
	// one empty claim, so signalling unconditionally keeps this correct and simple.
	e.signalWork()
	return nil
}

// prepareAdvance runs the once-per-tick setup before the task loop: it loads the
// definition, resolves config from the environment, locates the instance's current
// task, handles a lease-takeover reclaim (failing an interrupted only_once task), and
// emits work_started. It returns the loaded definition and the resolved task index, or
// a non-nil outcome the caller must return immediately (a failure).
func (e *Engine) prepareAdvance(inst *model.ProcessInstance) (*model.ProcessDefinition, int, *advanceOutcome) {
	// Load the definition once for the whole tick: it drives config resolution and
	// is the source of truth for the task list (the instance stores only its current
	// task id; successors are implied by definition order). An instance whose
	// definition cannot be loaded cannot run, so fail it with a clear reason.
	def, err := e.db.GetDefinition(inst.ProcessName, inst.ProcessVersion)
	if err != nil {
		return nil, 0, stop(e.failInstance(inst, fmt.Sprintf("load definition: %v", err)))
	}

	// Resolve config from the OS environment for this tick. Config is never
	// persisted — it is re-resolved every tick and exposed to expressions as
	// "config". A resolution failure (missing required var, bad coercion) fails
	// the instance with a clear reason.
	if def.ConfigSchema != nil {
		cfg, err := def.ResolveConfig(os.LookupEnv)
		if err != nil {
			return nil, 0, stop(e.failInstance(inst, fmt.Sprintf("config: %v", err)))
		}
		inst.Config = cfg
	}

	// Resolve the instance's position in the task list. An empty Task means it has
	// run off the end (nothing left) — the loop completes it. A non-empty Task that
	// isn't in the definition is a corrupt/mismatched row: fail it.
	idx := taskIndex(def.Tasks, inst.Task)
	if inst.Task != "" && idx < 0 {
		return nil, 0, stop(e.failInstance(inst, fmt.Sprintf("current task %q not found in definition", inst.Task)))
	}

	// Lease takeover: this instance was reclaimed from an expired lease, so its
	// current task may have started executing on the previous owner before it
	// crashed/stalled. Re-running is fine for idempotent tasks, but an only_once
	// (non-idempotent) call task cannot be safely re-executed — the call may already
	// have happened — so fail the instance to honour at-most-once.
	if inst.ReclaimedExpired {
		e.logOnly(logEvent{Level: model.LogWarn, ID: inst.ID,
			Msg:  "reclaimed expired lease; previous owner crashed or stalled mid-task",
			Meta: map[string]any{"task": inst.Task, "process": inst.ProcessName}})
		if idx >= 0 {
			s := def.Tasks[idx]
			if s.Action != nil && s.OnlyOnce != nil && *s.OnlyOnce {
				return nil, 0, stop(e.failInstance(inst, fmt.Sprintf(
					"task %q is only_once and was interrupted by a lease takeover; cannot re-execute", s.ID)))
			}
		}
	}

	// work_started: a worker has picked this instance up and is about to work its
	// current task. One per work session (a resume after parking emits it again),
	// tagged with the worker so the unified log shows who is doing what.
	if idx >= 0 {
		e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventWorkStarted, Task: inst.Task, Meta: map[string]any{"worker": e.workerID}})
	}

	return def, idx, nil
}

// advance executes the next task in the instance's queue, returning the outcome to
// persist (it performs no lease-releasing write itself — runAdvance does).
// Each task may have a call, a switch, or both. The call runs first; then the switch
// is evaluated with the call's output available as "self". A matching switch case
// jumps to the named task; no match advances to the next task in the queue.
func (e *Engine) advance(ctx context.Context, inst *model.ProcessInstance) advanceOutcome {
	if inst.Status == model.StatusFailing {
		return e.settleFailing(inst)
	}
	if inst.Status == model.StatusCancelling {
		return e.cancelInstance(inst)
	}

	def, idx, done := e.prepareAdvance(inst)
	if done != nil {
		return *done
	}

	// Process tasks in a loop. A call-less task (pure switch/routing) has no
	// external side effects, so once it resolves its goto we continue to the next
	// task in-memory without persisting — collapsing a chain of switch-only tasks
	// into a single claim and a single DB write at the boundary. We stop and
	// persist at the first task that has a call (child spawn or remote action), at
	// a terminal state, or after maxInlineTasks transitions (a guard against a
	// pathological all-switch loop holding the goroutine/lease forever).
	//
	// This is crash-safe: skipping persistence between call-less tasks is fine
	// because they only re-evaluate switches against already-persisted context, so
	// resuming from the last persisted task position is deterministic. Durable state
	// only changes at the boundaries (spawn txn, action result, terminal save), each
	// of which writes inst.Task — the current position in the definition's task list.
	const maxInlineTasks = 1000
	for i := 0; ; i++ {
		if idx < 0 || idx >= len(def.Tasks) {
			// Ran off the end of the task list: nothing left to do.
			inst.Task = ""
			inst.Status = model.StatusCompleted
			inst.WakeAt = nil
			if err := e.computeOutput(inst); err != nil {
				return e.failInstance(inst, err.Error())
			}
			e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventInstanceDone, Data: e.outputData(inst)})
			return advanceOutcome{kind: outcomeTerminal}
		}

		task := def.Tasks[idx]
		// Point the instance at the task about to run, so any mid-task persist (park,
		// retry, error route, fail) records this task as the resume point.
		inst.Task = task.ID
		hasCall := task.Action != nil
		var actionResult any

		// Capture this task's prior output before the action can overwrite it, so an
		// output map may reference self.previous (the value from the last loop iteration).
		var priorOutput any
		if task.Output.Present() {
			if outs, ok := inst.ContextData["outputs"].(map[string]any); ok {
				priorOutput = outs[task.ID]
			}
		}

		if hasCall {
			switch task.Action.Type {
			case model.ActionTypeChildMap, model.ActionTypeChildList:
				out, done := e.runChildProcesses(ctx, inst, task)
				if done != nil {
					return *done
				}
				actionResult = out
			case model.ActionTypeDelay:
				if done := e.runDelay(inst, task); done != nil {
					return *done
				}
				// Timer fired: fall through to the switch with no action result.
			case model.ActionTypeExternal:
				out, done := e.runExternal(ctx, inst, task)
				if done != nil {
					return *done
				}
				actionResult = out
			default: // rest, script
				out, done := e.executeAction(ctx, inst, task)
				if done != nil {
					return *done
				}
				actionResult = out
			}
		}

		// The output projection (if any) is the only thing exported (outputs.taskID).
		// The raw result is never stored; it is exposed transiently to this task's own
		// output/switch as self.result.
		var taskOutput any
		hasOutput := task.Output.Present()
		if hasOutput {
			remapped, err := e.evalTaskOutput(inst, task, actionResult, priorOutput)
			if err != nil {
				return e.failInstance(inst, fmt.Sprintf("task %q output: %v", task.ID, err))
			}
			e.setTaskOutput(inst, task.ID, remapped)
			taskOutput = remapped
		}

		// self is this task's transient scope: result (raw action result) and
		// previous (its own prior output), plus output (the projection) only when one
		// is defined. None of these but the projection persist beyond this task.
		self := map[string]any{"result": actionResult, "previous": priorOutput}
		if hasOutput {
			self["output"] = taskOutput
		}
		gotoID, err := e.evalSwitch(inst, task, self)
		if err != nil {
			return e.failInstance(inst, fmt.Sprintf("task %q switch: %v", task.ID, err))
		}
		if gotoID == "" {
			// Validation requires a catch-all case, but legacy rows in the DB may
			// predate that rule — fail the instance rather than panic on gotoID[1:].
			return e.failInstance(inst, fmt.Sprintf("task %q switch: no case matched", task.ID))
		}

		if gotoID == model.GotoEnd {
			inst.Status = model.StatusCompleted
			inst.RetryCount = 0
			inst.WakeAt = nil
			if err := e.computeOutput(inst); err != nil {
				return e.failInstance(inst, err.Error())
			}
			e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventInstanceDone, Task: task.ID, Data: e.outputData(inst)})
			return advanceOutcome{kind: outcomeTerminal}
		}

		if gotoID == model.GotoNext {
			idx++
		} else {
			// gotoID is a task reference like "$ship" — strip the sigil.
			if idx = taskIndex(def.Tasks, gotoID[1:]); idx < 0 {
				return e.failInstance(inst, fmt.Sprintf("goto task %q not found in %q v%d", gotoID[1:], inst.ProcessName, inst.ProcessVersion))
			}
		}
		// Reflect the new position (empty once we run past the last task) so a
		// checkpoint here persists the next task to run, not the one just completed.
		inst.Task = taskIDAt(def.Tasks, idx)

		inst.RetryCount = 0
		inst.WakeAt = nil
		e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventTaskCompleted, Task: task.ID, Msg: "→ " + gotoID})

		// A task with a call has just executed a side effect — checkpoint and yield.
		// A call-less routing task had none, so continue in-memory to the next task
		// unless we've hit the inline-task guard.
		if hasCall || i >= maxInlineTasks {
			return advanceOutcome{kind: outcomeProgress}
		}
	}
}

// evalTaskOutput evaluates a task's output map against the context plus self,
// where self.result is the raw action result and self.previous is this task's
// prior output (its value from the last loop iteration, or nil on the first run).
func (e *Engine) evalTaskOutput(inst *model.ProcessInstance, task *model.Task, result, previous any) (any, error) {
	self := map[string]any{"result": result, "previous": previous}
	return e.evalShapeCtx(inst, task.Output.Raw, self)
}

// setTaskOutput stores value as the task's exported output (outputs.taskID),
// recording the task in output_order the first time it produces output (a loop
// re-execution overwrites the value without re-appending).
func (e *Engine) setTaskOutput(inst *model.ProcessInstance, taskID string, value any) {
	if inst.ContextData["outputs"] == nil {
		inst.ContextData["outputs"] = map[string]any{}
	}
	outs := inst.ContextData["outputs"].(map[string]any)
	_, existed := outs[taskID]
	outs[taskID] = value
	if existed {
		return
	}
	appendOutputOrder(inst, taskID)
}

// appendOutputOrder appends id to the instance's output_order list, tolerating the
// []any shape the field takes after a JSON round-trip through engine_state.
func appendOutputOrder(inst *model.ProcessInstance, id string) {
	var order []string
	switch v := inst.ContextData["output_order"].(type) {
	case []string:
		order = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				order = append(order, s)
			}
		}
	}
	inst.ContextData["output_order"] = append(order, id)
}

// evalSwitch walks the task's switch cases in order and returns the Goto target
// of the first case whose Case expression evaluates to true. An empty Case is a
// catch-all that always matches and must be the last entry when present. Returns ""
// when the switch list is empty (should not happen on validated definitions).
func (e *Engine) evalSwitch(inst *model.ProcessInstance, task *model.Task, selfOutput any) (string, error) {
	for _, c := range task.Switch {
		if c.Case == "" {
			return c.Goto, nil
		}
		ok, err := e.evalBoolCtx(inst, c.Case, selfOutput)
		if err != nil {
			return "", fmt.Errorf("case %q: %w", c.Case, err)
		}
		if ok {
			return c.Goto, nil
		}
	}
	return "", nil
}

// taskIndex returns the position of taskID in tasks, or -1 if absent (the empty id —
// "no current task" — is always absent).
func taskIndex(tasks []*model.Task, taskID string) int {
	if taskID == "" {
		return -1
	}
	for i, t := range tasks {
		if t.ID == taskID {
			return i
		}
	}
	return -1
}

// taskIDAt returns the id of the task at idx, or "" when idx is out of range (the
// instance has advanced past the last task).
func taskIDAt(tasks []*model.Task, idx int) string {
	if idx < 0 || idx >= len(tasks) {
		return ""
	}
	return tasks[idx].ID
}

// resolveGoto validates that the instance's definition contains taskID, so the engine
// can point the instance at it. No queue is built — the remaining tasks are implied by
// definition order. Used by the on-error route, which has no definition in scope.
func (e *Engine) resolveGoto(inst *model.ProcessInstance, taskID string) error {
	def, err := e.db.GetDefinition(inst.ProcessName, inst.ProcessVersion)
	if err != nil {
		return fmt.Errorf("resolve goto: %w", err)
	}
	if taskIndex(def.Tasks, taskID) < 0 {
		return fmt.Errorf("goto task %q not found in %q v%d", taskID, inst.ProcessName, inst.ProcessVersion)
	}
	return nil
}

// saveAndNotify is the single exit point for all terminal instance states.
// For root instances and failed instances it saves directly; for non-failed child
// instances it uses FinishChild, which atomically saves the child and transitions
// the parent to WaitStateCollecting when all siblings are done.
func (e *Engine) saveAndNotify(inst *model.ProcessInstance) error {
	if inst.ParentID == "" {
		return e.db.UpdateInstance(inst)
	}
	if inst.Status == model.StatusFailed {
		return e.db.FailInstanceAndAncestors(inst)
	}
	return e.db.FinishChild(inst)
}

// computeOutput evaluates the process definition's Output expression map against
// the final context and stores the result in context_data["output"]. No-op if
// the definition has no Output map.
func (e *Engine) computeOutput(inst *model.ProcessInstance) error {
	def, err := e.db.GetDefinition(inst.ProcessName, inst.ProcessVersion)
	if err != nil {
		return fmt.Errorf("load definition for output: %w", err)
	}
	if !def.Output.Present() {
		return nil
	}
	out, err := e.evalShapeCtx(inst, def.Output.Raw, nil)
	if err != nil {
		return fmt.Errorf("output: %w", err)
	}
	inst.ContextData["output"] = out
	return nil
}
