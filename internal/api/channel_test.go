package api

import (
	"encoding/json"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func restDef(name string) map[string]any {
	return map[string]any{
		"name": name,
		"steps": []any{
			map[string]any{"id": "s1", "call": map[string]any{"type": "rest", "endpoint": "http://localhost/x"}},
		},
	}
}

func switchDef(name string) map[string]any {
	return map[string]any{
		"name": name,
		"steps": []any{
			map[string]any{"id": "s1", "switch": []any{
				map[string]any{"when": "default", "goto": "$end"},
			}},
		},
	}
}

func childProcessDef(name string, childName string, childVersion int) map[string]any {
	child := map[string]any{"name": childName}
	if childVersion != 0 {
		child["version"] = childVersion
	}
	return map[string]any{
		"name": name,
		"steps": []any{
			map[string]any{"id": "spawn", "call": map[string]any{
				"type":      "child_process",
				"processes": []any{child},
			}},
		},
	}
}

func batchApply(h *Handlers, channel string, autoUpdate bool, defs ...any) Reply {
	payload, _ := json.Marshal(map[string]any{
		"channel":             channel,
		"auto_update_parents": autoUpdate,
		"definitions":         defs,
	})
	return h.Handle(Envelope{Action: "put_definitions_batch", Payload: payload})
}

func putDef(h *Handlers, def map[string]any) Reply {
	payload, _ := json.Marshal(def)
	return h.Handle(Envelope{Action: "put_definition", Payload: payload})
}

func listChannels(h *Handlers, t *testing.T, name string) []ChannelEntry {
	t.Helper()
	r := h.Handle(Envelope{Action: "list_channels", Payload: mustMarshal(t, ListChannelsReq{Name: name})})
	if !r.OK {
		t.Fatalf("list_channels(%q): %s", name, r.Error)
	}
	var out []ChannelEntry
	json.Unmarshal(r.Data, &out)
	return out
}

func channelVersion(entries []ChannelEntry, channel string) int {
	for _, e := range entries {
		if e.Channel == channel {
			return e.Version
		}
	}
	return 0
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestApplyBatch_SetsChannel(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	r := batchApply(h, "stable", false, restDef("p"))
	if !r.OK {
		t.Fatalf("apply failed: %s", r.Error)
	}

	entries := listChannels(h, t, "p")
	if channelVersion(entries, "stable") != 1 {
		t.Errorf("expected stable→1, got %+v", entries)
	}
}

func TestApplyBatch_ContentDedup(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	batchApply(h, "latest", false, restDef("p"))

	r := batchApply(h, "latest", false, restDef("p"))
	if !r.OK {
		t.Fatalf("second apply failed: %s", r.Error)
	}
	var results []BatchApplyResult
	json.Unmarshal(r.Data, &results)
	if len(results) != 1 || results[0].Saved || results[0].Version != 1 {
		t.Errorf("expected Saved=false version=1 on identical content, got %+v", results)
	}
}

// Applying identical content a second time is deduplicated regardless of how
// many times it has been applied before.
func TestApplyBatch_ContentDedup_Idempotent(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	batchApply(h, "latest", false, restDef("p"))

	// Apply same content again — must report Saved=false.
	r := batchApply(h, "latest", false, restDef("p"))
	if !r.OK {
		t.Fatalf("apply failed: %s", r.Error)
	}
	var results []BatchApplyResult
	json.Unmarshal(r.Data, &results)
	if len(results) != 1 || results[0].Saved {
		t.Errorf("expected Saved=false (same content), got %+v", results)
	}
	// Channel should stay at v1.
	if channelVersion(listChannels(h, t, "p"), "latest") != 1 {
		t.Error("expected channel to remain at v1")
	}
}

func TestApplyBatch_ContentDedup_SelfRef(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	// A self-referential (recursive) process: calls itself as a child.
	selfRef := map[string]any{
		"name": "recursive",
		"steps": []any{
			map[string]any{"id": "recurse", "call": map[string]any{
				"type":      "child_process",
				"processes": []any{map[string]any{"name": "recursive"}},
			}},
		},
	}

	batchApply(h, "latest", false, selfRef)

	// Apply same content again — must not create a new version.
	r := batchApply(h, "latest", false, selfRef)
	if !r.OK {
		t.Fatalf("apply failed: %s", r.Error)
	}
	var results []BatchApplyResult
	json.Unmarshal(r.Data, &results)
	if len(results) != 1 || results[0].Saved {
		t.Errorf("expected Saved=false for self-referential process, got %+v", results)
	}
	if channelVersion(listChannels(h, t, "recursive"), "latest") != 1 {
		t.Error("expected channel to remain at v1")
	}
}

func TestApplyBatch_NewVersionOnChange(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	batchApply(h, "latest", false, restDef("p"))

	changed := restDef("p")
	changed["steps"] = []any{
		map[string]any{"id": "s1", "call": map[string]any{"type": "rest", "endpoint": "http://localhost/changed"}},
	}
	r := batchApply(h, "latest", false, changed)
	if !r.OK {
		t.Fatalf("apply failed: %s", r.Error)
	}
	var results []BatchApplyResult
	json.Unmarshal(r.Data, &results)
	if len(results) != 1 || !results[0].Saved || results[0].Version != 2 {
		t.Errorf("expected Saved=true version=2, got %+v", results)
	}
	if channelVersion(listChannels(h, t, "p"), "latest") != 2 {
		t.Error("expected channel to advance to v2")
	}
}

func TestApplyBatch_ChildVersionResolution(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	// Apply child on "latest".
	batchApply(h, "latest", false, switchDef("child"))

	// Apply parent with version=0 (omitted) child ref — should resolve to v1.
	r := batchApply(h, "latest", false, childProcessDef("parent", "child", 0))
	if !r.OK {
		t.Fatalf("parent apply failed: %s", r.Error)
	}
	// Channel for parent should be set.
	if channelVersion(listChannels(h, t, "parent"), "latest") != 1 {
		t.Error("expected parent@latest=1")
	}
}

func TestApplyBatch_ChildVersionResolution_ChildNotOnChannel(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	// Parent references a child that hasn't been applied to any channel yet.
	r := batchApply(h, "latest", false, childProcessDef("parent", "missing-child", 0))
	if r.OK {
		t.Error("expected error when child not on channel")
	}
	if !containsString(r.Error, "not on channel") {
		t.Errorf("expected 'not on channel' error, got %q", r.Error)
	}
}

func TestApplyBatch_TopoSort(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	// Parent first, child second — topo sort should handle it.
	r := batchApply(h, "latest", false,
		childProcessDef("parent", "child", 0),
		switchDef("child"),
	)
	if !r.OK {
		t.Fatalf("apply failed (expected topo sort to reorder): %s", r.Error)
	}
}

func TestApplyBatch_CycleDetection(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	a := childProcessDef("a", "b", 0)
	b := childProcessDef("b", "a", 0)
	r := batchApply(h, "latest", false, a, b)
	if r.OK {
		t.Error("expected error for cycle")
	}
	if !containsString(r.Error, "cycle") {
		t.Errorf("expected cycle error, got %q", r.Error)
	}
}

func TestApplyBatch_AutoUpdateParents_Basic(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	// Set up: child v1 + parent v1 on "stable".
	batchApply(h, "stable", false, switchDef("child"), childProcessDef("parent", "child", 0))

	// Apply child v2 with auto-update-parents.
	child2 := switchDef("child")
	child2["steps"] = []any{map[string]any{"id": "s2", "switch": []any{
		map[string]any{"when": "default", "goto": "$end"},
	}}}
	r := batchApply(h, "stable", true, child2)
	if !r.OK {
		t.Fatalf("apply child v2 failed: %s", r.Error)
	}

	var results []BatchApplyResult
	json.Unmarshal(r.Data, &results)
	names := map[string]int{}
	for _, res := range results {
		names[res.Name] = res.Version
	}
	if names["child"] != 2 {
		t.Errorf("expected child v2, got %v", names)
	}
	if names["parent"] < 2 {
		t.Errorf("expected parent to be bumped, got v%d", names["parent"])
	}

	// stable/parent must now point to the new version.
	entries := listChannels(h, t, "parent")
	if channelVersion(entries, "stable") < 2 {
		t.Errorf("expected stable/parent ≥ v2, got %+v", entries)
	}
}

func TestApplyBatch_AutoUpdateParents_Cascade(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	// leaf → parent → grandparent, all on "latest".
	batchApply(h, "latest", false,
		switchDef("leaf"),
		childProcessDef("parent", "leaf", 0),
		childProcessDef("grandparent", "parent", 0),
	)

	// Update leaf: grandparent should also cascade.
	leaf2 := switchDef("leaf")
	leaf2["steps"] = []any{map[string]any{"id": "s2", "switch": []any{
		map[string]any{"when": "default", "goto": "$end"},
	}}}
	r := batchApply(h, "latest", true, leaf2)
	if !r.OK {
		t.Fatalf("apply failed: %s", r.Error)
	}

	var results []BatchApplyResult
	json.Unmarshal(r.Data, &results)
	names := map[string]int{}
	for _, res := range results {
		names[res.Name] = res.Version
	}
	if names["grandparent"] < 2 {
		t.Errorf("expected grandparent to cascade, got v%d", names["grandparent"])
	}
}

func TestApplyBatch_AutoUpdateParents_OtherChannelUntouched(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	// child on "latest", parent on "stable" (different channel).
	batchApply(h, "latest", false, switchDef("child"))
	batchApply(h, "stable", false, switchDef("child"), childProcessDef("parent", "child", 0))

	// Update child on "latest" only.
	child2 := switchDef("child")
	child2["steps"] = []any{map[string]any{"id": "s2", "switch": []any{
		map[string]any{"when": "default", "goto": "$end"},
	}}}
	batchApply(h, "latest", true, child2)

	// "stable" channel for parent must still be v1.
	entries := listChannels(h, t, "parent")
	if v := channelVersion(entries, "stable"); v != 1 {
		t.Errorf("expected stable/parent to remain v1, got v%d", v)
	}
}

// ── channel CRUD ──────────────────────────────────────────────────────────────

func TestChannelCRUD(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	putDef(h, restDef("p"))

	// put_channel
	pc := h.Handle(Envelope{Action: "put_channel", Payload: mustMarshal(t, PutChannelReq{Name: "p", Channel: "stable", Version: 1})})
	if !pc.OK {
		t.Fatalf("put_channel: %s", pc.Error)
	}

	entries := listChannels(h, t, "p")
	if channelVersion(entries, "stable") != 1 {
		t.Errorf("expected stable→1, got %+v", entries)
	}

	// Update channel pointer — putDef creates v2 (server-assigned).
	putDef(h, restDef("p"))
	h.Handle(Envelope{Action: "put_channel", Payload: mustMarshal(t, PutChannelReq{Name: "p", Channel: "stable", Version: 2})})
	if channelVersion(listChannels(h, t, "p"), "stable") != 2 {
		t.Error("expected stable to advance to v2")
	}

	// delete_channel
	dc := h.Handle(Envelope{Action: "delete_channel", Payload: mustMarshal(t, DeleteChannelReq{Name: "p", Channel: "stable"})})
	if !dc.OK {
		t.Fatalf("delete_channel: %s", dc.Error)
	}
	if channelVersion(listChannels(h, t, "p"), "stable") != 0 {
		t.Error("expected stable to be gone after delete")
	}
}

func TestPutChannel_RequiresExistingDefinition(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	r := h.Handle(Envelope{Action: "put_channel", Payload: mustMarshal(t, PutChannelReq{Name: "ghost", Channel: "stable", Version: 1})})
	if r.OK {
		t.Error("expected error for non-existent definition")
	}
}

// ── promote_channel ───────────────────────────────────────────────────────────

func TestPromoteChannel_CopiesAll(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	batchApply(h, "staging", false, restDef("a"), restDef("b"))

	pr := h.Handle(Envelope{Action: "promote_channel", Payload: mustMarshal(t, PromoteChannelReq{From: "staging", To: "latest"})})
	if !pr.OK {
		t.Fatalf("promote_channel: %s", pr.Error)
	}

	for _, name := range []string{"a", "b"} {
		if channelVersion(listChannels(h, t, name), "latest") != 1 {
			t.Errorf("expected %s@latest=1 after promotion", name)
		}
	}
}

func TestPromoteChannel_StagingPreserved(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	batchApply(h, "staging", false, restDef("p"))
	h.Handle(Envelope{Action: "promote_channel", Payload: mustMarshal(t, PromoteChannelReq{From: "staging", To: "latest"})})

	// staging pointer must still exist.
	if channelVersion(listChannels(h, t, "p"), "staging") != 1 {
		t.Error("expected staging to survive after promotion")
	}
}

func TestPromoteChannel_Subtree(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	batchApply(h, "staging", false, switchDef("child"), childProcessDef("parent", "child", 0), restDef("unrelated"))

	proc := "parent"
	pr := h.Handle(Envelope{Action: "promote_channel", Payload: mustMarshal(t, PromoteChannelReq{From: "staging", To: "latest", Process: &proc})})
	if !pr.OK {
		t.Fatalf("promote_channel: %s", pr.Error)
	}

	// parent and its child should be promoted.
	if channelVersion(listChannels(h, t, "parent"), "latest") != 1 {
		t.Error("expected parent@latest=1")
	}
	if channelVersion(listChannels(h, t, "child"), "latest") != 1 {
		t.Error("expected child@latest=1 (dependency of parent)")
	}
	// unrelated should NOT be promoted.
	if channelVersion(listChannels(h, t, "unrelated"), "latest") != 0 {
		t.Error("expected unrelated to not be promoted")
	}
}

// ── channel_status ────────────────────────────────────────────────────────────

func TestChannelStatus_Clean(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	batchApply(h, "stable", false, switchDef("child"), childProcessDef("parent", "child", 0))

	cs := h.Handle(Envelope{Action: "channel_status", Payload: mustMarshal(t, ChannelStatusReq{Channel: "stable"})})
	if !cs.OK {
		t.Fatalf("channel_status: %s", cs.Error)
	}
	var items []ChannelStatusItem
	json.Unmarshal(cs.Data, &items)
	for _, item := range items {
		if len(item.StaleRefs) > 0 {
			t.Errorf("expected no stale refs, got %+v for %s", item.StaleRefs, item.Name)
		}
	}
}

func TestChannelStatus_StaleRef(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	// Apply child v1 + parent v1 (baked with child@v1).
	batchApply(h, "stable", false, switchDef("child"), childProcessDef("parent", "child", 0))

	// Advance child to v2 on stable WITHOUT updating parent.
	child2 := switchDef("child")
	child2["steps"] = []any{map[string]any{"id": "s2", "switch": []any{
		map[string]any{"when": "default", "goto": "$end"},
	}}}
	batchApply(h, "stable", false, child2)

	cs := h.Handle(Envelope{Action: "channel_status", Payload: mustMarshal(t, ChannelStatusReq{Channel: "stable"})})
	if !cs.OK {
		t.Fatalf("channel_status: %s", cs.Error)
	}
	var items []ChannelStatusItem
	json.Unmarshal(cs.Data, &items)

	var parentItem *ChannelStatusItem
	for i := range items {
		if items[i].Name == "parent" {
			parentItem = &items[i]
		}
	}
	if parentItem == nil {
		t.Fatal("parent not in channel_status response")
	}
	if len(parentItem.StaleRefs) == 0 {
		t.Fatal("expected parent to have stale refs")
	}
	ref := parentItem.StaleRefs[0]
	if ref.ChildName != "child" || ref.BakedVersion != 1 || ref.ChannelVersion != 2 {
		t.Errorf("unexpected stale ref: %+v", ref)
	}
}

// ── start_instance with channel ───────────────────────────────────────────────

func TestStartInstance_Channel(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	// v1 on "stable", v2 on "latest" (different content).
	batchApply(h, "stable", false, restDef("p"))
	batchApply(h, "latest", false, func() map[string]any {
		d := restDef("p")
		d["steps"] = []any{map[string]any{"id": "s1", "call": map[string]any{"type": "rest", "endpoint": "http://localhost/v2"}}}
		return d
	}())

	ch := "stable"
	si := h.Handle(Envelope{Action: "start_instance", Payload: mustMarshal(t, StartInstanceReq{
		Process: "p",
		Channel: &ch,
	})})
	if !si.OK {
		t.Fatalf("start_instance: %s", si.Error)
	}
	var resp StartInstanceResp
	json.Unmarshal(si.Data, &resp)
	if resp.Version != 1 {
		t.Errorf("expected version 1 (stable), got %d", resp.Version)
	}
}

func TestStartInstance_ExplicitVersionTakesPriority(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	batchApply(h, "stable", false, restDef("p"))
	batchApply(h, "latest", false, func() map[string]any {
		d := restDef("p")
		d["steps"] = []any{map[string]any{"id": "s1", "call": map[string]any{"type": "rest", "endpoint": "http://localhost/v2"}}}
		return d
	}())

	ch := "stable"
	v := 2
	si := h.Handle(Envelope{Action: "start_instance", Payload: mustMarshal(t, StartInstanceReq{
		Process: "p",
		Version: &v,
		Channel: &ch,
	})})
	if !si.OK {
		t.Fatalf("start_instance: %s", si.Error)
	}
	var resp StartInstanceResp
	json.Unmarshal(si.Data, &resp)
	if resp.Version != 2 {
		t.Errorf("expected version 2 (explicit overrides channel), got %d", resp.Version)
	}
}

func TestStartInstance_InvalidChannel(t *testing.T) {
	h, cleanup := newTestHandlers(t)
	defer cleanup()

	putDef(h, restDef("p"))

	ch := "nonexistent"
	r := h.Handle(Envelope{Action: "start_instance", Payload: mustMarshal(t, StartInstanceReq{
		Process: "p",
		Channel: &ch,
	})})
	if r.OK {
		t.Error("expected error for non-existent channel")
	}
}
