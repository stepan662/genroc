package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"genroc/internal/idgen"
	"genroc/internal/model"
)

// runChildProcesses handles the two-phase child lifecycle:
//
//  1. WaitStateNone → spawn children, suspend parent (wait_state='waiting').
//  2. WaitStateCollecting → all children are terminal; merge their outputs into
//     parent context and return so advance() continues past this task.
//
// A cancelling parent spawns cancelling children: they self-cancel and call
// FinishChild, which transitions the parent to WaitStateCollecting normally.
func (e *Engine) runChildProcesses(ctx context.Context, inst *model.ProcessInstance, task *model.Task) (any, *advanceOutcome) {
	// Phase 2: parent woke up with children done — collect their outputs into the
	// action result (self.result). It is exported only if the task projects it.
	if inst.WaitState == model.WaitStateCollecting {
		output, err := e.collectChildOutputs(ctx, inst, task)
		if err != nil {
			inst.WaitState = model.WaitStateNone
			return nil, stop(e.failInstance(inst, fmt.Sprintf("task %q collect: %v", task.ID, err)))
		}
		inst.WaitState = model.WaitStateNone
		e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventChildrenCollect, Task: task.ID})
		return output, nil
	}

	// Phase 1: spawn children. Record the spawned child IDs under the internal
	// "_children" key (keyed by task, then by child key for parallel) so observers
	// can correlate a parent task with its children. This is metadata only — child
	// results flow to self.result at collection, not into outputs.
	childCallStack := append(inst.CallStack, inst.ID)
	if inst.ContextData["_children"] == nil {
		inst.ContextData["_children"] = map[string]any{}
	}
	spawned := inst.ContextData["_children"].(map[string]any)

	var children []*model.ProcessInstance
	switch task.Action.Type {
	case model.ActionTypeChildMap:
		mapped, fail := e.buildMapChildren(ctx, inst, task, childCallStack)
		if fail != nil {
			return nil, fail
		}
		ids := make(map[string]any, len(mapped))
		for _, c := range mapped {
			key, _ := c.ContextData["_spawn_child_key"].(string)
			ids[key] = c.ID
		}
		spawned[task.ID] = ids
		children = mapped
	case model.ActionTypeChildList:
		listChildren, fail := e.buildListChildren(ctx, inst, task, childCallStack)
		if fail != nil {
			return nil, fail
		}
		if len(listChildren) == 0 {
			// Empty `over` array: there is nothing to spawn. Yield an empty-array
			// result and continue inline — do NOT park. SpawnChildrenAndWait is a
			// no-op on zero children, so parking here would leave the parent to
			// re-run this task forever.
			spawned[task.ID] = []any{}
			e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventChildrenSpawned, Task: task.ID, Msg: "0 children"})
			return []any{}, nil
		}
		ids := make([]any, len(listChildren))
		for i, c := range listChildren {
			ids[i] = c.ID
		}
		spawned[task.ID] = ids
		children = listChildren
	}

	appendOutputOrder(inst, task.ID)

	inst.RetryCount = 0
	inst.WakeAt = nil

	// A child spawn is a multi-row transaction that parks the parent atomically, so
	// it persists itself here rather than through runAdvance. The parent ends
	// 'waiting' (non-runnable), so dropping the marker after this write is harmless;
	// it reports outcomeNoop so runAdvance does no further write. On failure it
	// transitions to the terminal outcome instead.
	if err := e.db.SpawnChildrenAndWait(ctx, inst, children); err != nil {
		return nil, stop(e.failInstance(inst, fmt.Sprintf("task %q spawn: %v", task.ID, err)))
	}

	e.audit(inst, logEvent{Level: model.LogInfo, Event: model.EventChildrenSpawned, Task: task.ID, Msg: fmt.Sprintf("%d children", len(children))})
	// Each spawned child is its own process: record its creation + input so its
	// subtree trail bookends the same way a root's does.
	for _, c := range children {
		e.AuditCreated(c)
	}
	return nil, stop(advanceOutcome{kind: outcomeNoop})
}

// buildMapChildren resolves definitions, evaluates inputs, and constructs
// ProcessInstances for all keyed (child_map) children. Does not persist anything; a
// non-nil outcome means the parent failed and the caller should stop and persist it.
// resolveChildVersion picks the version to spawn a child at. A non-zero declared
// version is used as-is. Otherwise a self-reference (same process name) inherits the
// parent's version; a cross-process reference resolves the version pinned for this task
// at registration (GetDependencyVersion), falling back to the child's latest published
// version. depKey is the child_map key ("" for child_list).
func (e *Engine) resolveChildVersion(inst *model.ProcessInstance, taskID, name string, declared int, depKey string) (int, error) {
	if declared != 0 {
		return declared, nil
	}
	if name == inst.ProcessName {
		return inst.ProcessVersion, nil
	}
	if v, err := e.db.GetDependencyVersion(inst.ProcessName, inst.ProcessVersion, taskID, depKey); err == nil {
		return v, nil
	}
	return e.db.LatestVersion(name)
}

// newChildInstance builds a running child ProcessInstance rooted at parent. id is the
// caller-assigned sibling id (base+i of the batch's ChildBase, so siblings form a
// contiguous run that sorts after the parent and among itself in spawn order). spawnCtx
// carries the per-type discriminant keys (_spawn_action_type plus _spawn_child_key /
// _spawn_index, and _spawn_result_schema when declared), merged over the common child
// context.
func newChildInstance(parent *model.ProcessInstance, task *model.Task, def *model.ProcessDefinition, version int, input any, callStack []string, id string, spawnCtx map[string]any) *model.ProcessInstance {
	childCtx := map[string]any{
		"input":        input,
		"outputs":      map[string]any{},
		"output_order": []string{},
		"error":        nil,
	}
	for k, v := range spawnCtx {
		childCtx[k] = v
	}
	return &model.ProcessInstance{
		ID:             id,
		ProcessName:    def.Name,
		ProcessVersion: version,
		Task:           def.Tasks[0].ID,
		ContextData:    childCtx,
		Status:         model.StatusRunning,
		ParentID:       parent.ID,
		SpawnTaskID:    task.ID,
		CallStack:      callStack,
	}
}

func (e *Engine) buildMapChildren(ctx context.Context, inst *model.ProcessInstance, task *model.Task, callStack []string) ([]*model.ProcessInstance, *advanceOutcome) {
	keys := make([]string, 0, len(task.Action.Children))
	for key := range task.Action.Children {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// One base id (guaranteed to sort after the parent); siblings are base, base+1,
	// … in sorted-key order, so the whole batch sorts after the parent and among
	// itself in spawn order.
	base := idgen.ChildBase(inst.ID)

	children := make([]*model.ProcessInstance, 0, len(task.Action.Children))
	for i, key := range keys {
		entry := task.Action.Children[key]
		version, err := e.resolveChildVersion(inst, task.ID, entry.Name, entry.Version, key)
		if err != nil {
			return nil, stop(e.failInstance(inst, fmt.Sprintf("task %q child_map[%q]: %v", task.ID, key, err)))
		}
		def, err := e.db.GetDefinition(entry.Name, version)
		if err != nil {
			return nil, stop(e.failInstance(inst, fmt.Sprintf("task %q child_map[%q]: %v", task.ID, key, err)))
		}
		input, err := e.evalChildInput(inst, task.ID, fmt.Sprintf("child_map[%q]", key), entry.Input)
		if err != nil {
			return nil, stop(e.failInstance(inst, err.Error()))
		}
		input, err = def.ValidateInput(input)
		if err != nil {
			return nil, stop(e.failInstance(inst, fmt.Sprintf("task %q child_map[%q] input validation: %v", task.ID, key, err)))
		}
		spawnCtx := map[string]any{
			"_spawn_action_type": string(model.ActionTypeChildMap),
			"_spawn_child_key":   key,
		}
		if entry.ResultSchema != nil {
			if b, err := json.Marshal(entry.ResultSchema); err == nil {
				spawnCtx["_spawn_result_schema"] = string(b)
			}
		}
		children = append(children, newChildInstance(inst, task, def, version, input, callStack, idgen.Add(base, uint64(i)).String(), spawnCtx))
	}
	return children, nil
}

// buildListChildren evaluates the child_list `over` expression to an array and
// constructs one child ProcessInstance per element, in order — each element is that
// child's input. It returns an empty slice (no error) when `over` yields an empty
// array or null; the caller handles that as the empty-fan-out case. Does not persist
// anything; a non-nil outcome means the parent failed and the caller should stop and
// persist it.
func (e *Engine) buildListChildren(ctx context.Context, inst *model.ProcessInstance, task *model.Task, callStack []string) ([]*model.ProcessInstance, *advanceOutcome) {
	version, err := e.resolveChildVersion(inst, task.ID, task.Action.Name, task.Action.Version, "")
	if err != nil {
		return nil, stop(e.failInstance(inst, fmt.Sprintf("task %q child_list: %v", task.ID, err)))
	}
	def, err := e.db.GetDefinition(task.Action.Name, version)
	if err != nil {
		return nil, stop(e.failInstance(inst, fmt.Sprintf("task %q child_list: %v", task.ID, err)))
	}

	// Evaluate `over` to the input array. Registration guarantees a non-null array
	// type, but guard defensively: a null evaluates to the empty fan-out.
	arrVal, err := e.evalAnyCtx(inst, task.Action.Over)
	if err != nil {
		return nil, stop(e.failInstance(inst, fmt.Sprintf("task %q child_list over: %v", task.ID, err)))
	}
	if arrVal == nil {
		return nil, nil
	}
	items, ok := arrVal.([]any)
	if !ok {
		return nil, stop(e.failInstance(inst, fmt.Sprintf("task %q child_list: over did not evaluate to an array (got %T)", task.ID, arrVal)))
	}

	var resultSchema string
	if task.Action.ResultSchema != nil {
		if b, err := json.Marshal(task.Action.ResultSchema); err == nil {
			resultSchema = string(b)
		}
	}

	// One base id (sorts after the parent); siblings are base, base+1, … in element
	// order, so the batch sorts after the parent and among itself in input order.
	base := idgen.ChildBase(inst.ID)
	children := make([]*model.ProcessInstance, 0, len(items))
	for i, elem := range items {
		input, err := def.ValidateInput(elem)
		if err != nil {
			return nil, stop(e.failInstance(inst, fmt.Sprintf("task %q child_list[%d] input validation: %v", task.ID, i, err)))
		}
		spawnCtx := map[string]any{
			"_spawn_action_type": string(model.ActionTypeChildList),
			"_spawn_index":       i,
		}
		if resultSchema != "" {
			spawnCtx["_spawn_result_schema"] = resultSchema
		}
		children = append(children, newChildInstance(inst, task, def, version, input, callStack, idgen.Add(base, uint64(i)).String(), spawnCtx))
	}
	return children, nil
}

func (e *Engine) evalChildInput(inst *model.ProcessInstance, taskID, label string, input *model.Shape) (any, error) {
	if !input.Present() {
		return map[string]any{}, nil
	}
	val, err := e.evalShapeCtx(inst, input.Raw, nil)
	if err != nil {
		return nil, fmt.Errorf("task %q %s input: %v", taskID, label, err)
	}
	return val, nil
}
