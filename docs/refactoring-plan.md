# Refactoring plan — dedup & simplification

Tracking sweep for duplicated / over-long / generalizable code, grouped by risk.
Findings came from a per-subsystem audit (engine, api, db, schema/validation, model/cli).
Intentional duplication (dual-engine SQL, the test-oracle validator, paired
encode/decode symmetry, per-contract `snippet`/`resolveURL` families) is deliberately
left alone.

Status legend: ☐ todo · ◐ in progress · ☑ done

---

## Tier 1 — quick wins (mechanical, low risk) — ☑ DONE (committed)

- ☑ Use existing `db.forUpdate()` helper instead of 5 inline `lock := ""; if postgres` blocks — `db_lifecycle.go`, `db_signals.go`, `db_external.go`
- ☑ Drop dead `error` return from `updateInstanceParams` (+ 4 callers) — `db_instances.go`
- ☑ `serverFlag()` helper for the 14 copy-pasted `fs.String("server", …)` lines — `cmd/genctl/main.go`
- ☑ `forEachExpr()` iterator for the 4–5 copies of the `{{ … }}` scanner — `internal/template/template.go`
- ☑ `decode[T]()` generic for the 17 handler payload-unmarshal blocks — `internal/api/handlers.go`
- ☑ `resolveAndValidateChildOutput()` — dedup the two collect functions — `internal/engine/collect.go`
- ☑ `appendOutputOrder()` helper for the duplicated `output_order` normalize+append — `internal/engine/engine.go`

---

## Tier 2 — structural dedup (good value, needs test attention)

- ☑ **`runPage[T]` list-page runner** — all 6 list methods now route through one generic
  runner + `rowScanner` alias in `paginate.go` (query→scan-loop→orient→pageInfo).
  `ListInstances`/`queryInstancePage`, `ListDefinitions`/`ListChannels`,
  `ListLogs`/`ListTreeLogs` (converted `scanLogPage`→per-row `scanLogRow`). ~90 lines
  of boilerplate removed; full test suite green.
- ☑ **`withTx()` transaction wrapper** — added in `pg_rewriter.go` (next to `beginTx`);
  the begin/defer-rollback/commit dance now lives in one audited helper. Converted the 9
  error-only methods: `SaveInstance`/`UpdateInstance`/`UpdateInstanceProgress`
  (`db_instances.go`), `SaveDefinition` (`db_registry.go`, post-commit cache-delete kept
  outside the tx), `FinishChild`/`FailInstanceAndAncestors`/`CancelProcess`/
  `SpawnChildrenAndWait` (`db_lifecycle.go`), `ResolveExternalTask` (`db_external.go`).
  Deliberately left: `RetryProcess` (~200 lines; wrapping adds nesting for ~4 lines saved),
  `ClaimInstances` (bespoke select-close-update), and the two signal methods (multi-return).
  Full SQLite suite green; PG run pending (local Postgres not up).
- ☑ **Dedup child-spawn** — extracted `resolveChildVersion()` (the version lookup) and
  `newChildInstance()` (the shared ContextData + ProcessInstance literal) in `engine.go`;
  `buildMapChildren`/`buildListChildren` now differ only in their genuine per-type logic
  (map keys + evalChildInput vs. `over` array eval). Note: `ChildBase` is non-deterministic
  (mints a fresh v7), so `base` is still computed once per batch in each caller and the
  sibling id is passed in — the contiguous-run invariant is preserved. Full suite green.
- ☑ **`resolveChild()` in validation** — extracted the shared child-def-resolution block
  from `validateChildEntry`/`validateChildListEntry` (`validate_children.go`); callers wrap
  the returned error with their own prefix.
- ☑ **Default envelope sets `ID` from path** — `envelope()` now sets `ID: r.PathValue("id")`
  (`""` when no `{id}`), so `signal_instance`/`cancel_instance` dropped their custom
  `fromHTTP` closures (`actions.go`).
- ☑ **Embed `InstanceSummaryResp` in `InstanceStatusResp`** (`handlers.go`) — one source
  for the 8 shared fields. The OpenAPI reflector inlines the embed, so `openapi.json` is
  byte-identical (no contract change).
- ☑ **`binOperands()` / `unaryOperand()` guard helpers** (`inferops.go`) — the null-check +
  `concreteTypeOf` + ambiguity guard now lives in one place; 6 binary + 2 unary ops thread
  their op-specific messages (via `errors.New`, dodging the non-constant-format vet warning;
  the `%` for mod is pre-resolved). All error strings kept byte-identical.
- ☑ **Decompose `validateTask`** into `validateActionRequiredFields`/`validateSwitch`/
  `validateOnError`/`validateActionSchemas` (`definition.go`) — same checks, same order,
  same messages.
- ☑ **Hoist shared `wire` structs** — package-level `switchWireCase` / `errorCaseWire`
  replace the four inline copies across `SwitchMap`/`ErrorCase` marshal+unmarshal
  (`definition.go`); drift risk gone.

**Tier 2 complete** — full suite (Go + 206 TS integration tests) green; `openapi.json`
unchanged; `go vet` + `gofmt` clean.

---

## Tier 3 — larger / higher-risk (plan deliberately, one at a time)

- ☐ **Reflective param binder + typed action adapters (API)** — GET params are declared
  twice (struct tags + hand-written `fromHTTP`) and round-tripped through
  `json.Marshal`→`Unmarshal`. A `bindParams(r, &dst)` binder removes ~8 closures + the
  round-trips (~60+ lines) and kills the two-places-to-edit drift. New machinery — roll
  out endpoint-by-endpoint.
- ☐ **`computeContextSets` dataflow fixpoints** (`validation/context.go`) — 4 pairwise-dual
  worklist loops + a redundant recomputation; one parameterized `boolFlowFixpoint`
  removes ~100 lines. Highest single reduction, medium-high risk (subtle must/may
  start-edge asymmetries) — needs strong coverage.
- ☐ **Decompose `advance()`** (~200 lines) — split one-time pre-loop setup from the
  per-task loop (`engine.go`). Med (intricate early-return control flow).
- ☐ **Unify goto parse/format** — `SwitchCase.Goto` keeps the `$` prefix while
  `ErrorCase.Goto` strips it; validated 4 different ways. Touches an internal stored
  form, so every engine reader must move together.

---

## Cross-cutting theme

The **map-vs-list child duality** is duplicated at three layers: spawning
(`buildMapChildren`/`buildListChildren`), collection (fixed in Tier 1), and validation
(`validateChildEntry`/`validateChildListEntry`). Tier 2's child-spawn + `resolveChild`
items chip at two of the three; unifying the version-resolution logic across the engine
and validation packages would be the deepest cleanup.
