# Child → parent error handling

Status: **draft.** Nothing here is implemented, and the design is not frozen —
it is written to spec grade so the consequences are visible, not because the
decisions are final. Addresses `ROADMAP.md` → "think about error handling
child -> parent".

Settled enough to build on: the `raise` / `panic` / `end` trichotomy (§0), errors
as codes without payloads, defects never being catchable, and settle-all batch
resolution.

Still open, and worth deciding before implementation:

- **D3** — reachability-only checking, no exhaustiveness. The one decision that
  is expensive to reverse: adding exhaustiveness later breaks existing
  definitions, while removing it later breaks nothing.
- **D7** — no parent-side retry. Cheap to reverse; §10.1 describes the coarse
  workaround and the signal that would justify building the real thing.
- **§11.3** — whether revive should clear the `$error` context slot, not just the
  `error` column. Pre-existing inconsistency, now reachable one more way.
- **§11.5** — `errored` collides with the roadmap's existing use of the word to
  mean *faulted*. Worth settling alongside the `cancel → pause` rename.

## 0. Governing principle

> **An error is a branch slot, not a value.**

A raised error is a child saying one specific thing:

> *There is a condition I anticipated, it prevents me from finishing, and I want
> my parent to be able to react to it.*

That is the whole contract. It carries a code so the parent can branch on it and
a message so a human can read it — nothing else. If the parent needs data back,
it was not an error; it was an outcome, and it belongs in `output`.

Two corollaries drive every rule below.

**Anything unanticipated panics.** An uncaught action failure, a bad config, a
missing definition, a bug — the process fails immediately and takes its tree with
it. There is no catchable form of it, no flag, no wildcard. The only way to make
a failure reactable is to convert it into a raise *inside the child*, at the task
that actually understands what went wrong (§5.4).

A definition can say this deliberately too. Three clauses terminate a process,
and they cover the outcome space exactly once each:

| clause | status | meaning | caller can react |
|---|---|---|---|
| `goto: end` | `completed` | finished; produces `output` | — |
| `raise: {code, message}` | `errored` | an anticipated condition stopped me | **yes**, by naming the code |
| `panic: {code, message}` | `failed` | something is wrong that I did not anticipate | **no**, ever |

Both failure clauses carry a code, but for different readers. A raised code is
consumed *inside* the tree, by a parent that branches on it. A panic code is
consumed *outside* it — by the API, dashboards and alerting, which need a stable
discriminator to count and filter on. Reactability and classification are
separate concerns, and only the first is denied to a panic.

**Errors are for branching, not for retrying around.** Transient conditions
belong to the child, which retries them internally and raises only once it has
given up. An error that crosses a process boundary describes a *settled*
condition, so there is no parent-side retry at all (D7) — and "catch everything
and retry" is not expressible here.

This does **not** replace the `{ok: false, reason: …}` output convention; the two
coexist for different jobs. "8 of 10 shipped, and here is why 2 didn't" is a
*result* and stays in child output. Errors are for control flow only.

## 1. Vocabulary

| term | meaning |
|---|---|
| **batch** | all children spawned by one `(parent_id, spawn_task_id)` pair |
| **slot** | a stable position in a batch: `key` for `child_map`, `index` for `child_list` |
| **raise** | a child terminating via a `raise` clause → status `errored` |
| **panic** | a child terminating via a `panic` clause → status `failed`; an *authored* defect |
| **defect** | any termination as `failed`, authored or engine-produced — never catchable |
| **raise set** | `raises(D)`, the codes a definition `D` can raise (§2.3) |
| **child task** | a task whose action is `child_map` or `child_list` |
| **resolution** | the parent's decision procedure over a settled batch (§5.2) |

## 2. Surface syntax

### 2.1 `raise` — an explicit clause, not a goto

A `raise` clause terminates the process with an error. It is a **field, not a
goto target**, so the code and its message are structurally paired and neither
can drift from the other:

```yaml
name: charge-card
tasks:
  - id: charge
    action: { type: fetch, url: "{{ config.psp_url }}/charge", result_schema: { … } }
    on_error:
      - code: ["http.402"]
        raise:
          code: card_declined
          message: "the issuer declined the charge"
    switch:
      - case: "self.result.status == 'no_funds'"
        raise:
          code: insufficient_funds
          message: "the account has insufficient funds"
      - goto: end
```

```go
// Fault is a terminal error: a machine-readable code and a human-readable
// message, both static. Used by both `raise` and `panic` — they differ in what
// they do, not in what they carry.
type Fault struct {
    Code    string `json:"code"    validate:"required" description:"Error code, lower_snake_case, no dots. A literal — never an expression."`
    Message string `json:"message" validate:"required" description:"Human-readable message. A literal string — never an expression."`
}
```

One shared type rather than two identical ones: they carry the same thing for
the same reasons, and two structs that must stay in lockstep is a drift hazard
for no gain. The distinction lives in the field name at the use site
(`Raise *Fault` / `Panic *Fault`), which is where a reader looks anyway.

`raise` is valid on a `SwitchCase` and on an `ErrorCase`, and is **mutually
exclusive with `goto`** on both (R3): a case either routes or raises.

`message` is **required** (R1). One line of prose per failure mode is cheap, and
it is what an operator reads off the instance detail before anything else.

Both fields are literals. An expression in either would re-open the data channel
this design exists to close (R2).

### 2.2 `panic` — authoring a defect

`panic` terminates the process as `failed`. It is the definition-level form of
everything in §0's first corollary: *I have detected something I did not
anticipate, and nothing downstream should try to work around it.*

```yaml
  - id: submit
    action:
      type: fetch
      url: "{{ config.api }}/submit"
      result_schema: { … }
    switch:
      - case: "self.result.status == 'rejected'"
        raise:
          code: submission_rejected      # anticipated — the caller may react
          message: "the service rejected the submission"
      - case: "self.result.error != null"
        panic:                                    # not anticipated
          code: submit_contract_violation
          message: "the service returned 200 with an error body"
      - goto: end
```

`panic` carries the same `Fault` as `raise`. Its code is not there to be caught —
nothing can catch a panic — but to be **classified** by everything outside the
process tree: `error_code` is a filterable column, so an operator can count
`submit_contract_violation` across instances, alert on it, or spot a spike after
a deploy. Burying that in free text would make the API strictly less useful for
the failures people most need to see.

This is the case the HTTP layer cannot see for you: a `200` with an error body
satisfies `accepted_status`, so no `http.NNN` code is ever produced and
`on_error` never fires. Only the switch, reading the parsed body, can tell that
the call actually failed — and only the author knows whether that particular body
is an anticipated condition (`raise`) or a broken contract (`panic`).

`panic` is valid wherever `raise` is — a `SwitchCase` or an `ErrorCase` — and is
mutually exclusive with both `goto` and `raise` (R3). In an `on_error` rule it is
mostly a way to *document* a fatal code ahead of a more permissive later rule, or
to replace the engine's generic failure text with something a human can act on:

```yaml
    on_error:
      - code: ["http.401"]
        panic:
          code: bad_credentials
          message: "credentials rejected — check GENROC_<proc>_API_TOKEN"
      - code: ["http.5%"]
        retries: 3
        goto: $fallback
```

**Why "panic" and not "fail".** The status it produces is `failed`, so the names
do not match — deliberately. `panic` is alarming, and it should be: it is
uncatchable and it takes the whole tree down. A milder spelling would invite
reaching for it instead of thinking about whether the condition deserves a
`raise`.

### 2.3 The raise set is inferred

There is no `errors:` declaration block. A process's raise set is derived by a
purely syntactic scan:

```
raises(D) := { f.Code : f is the Fault of a *raise* clause on any
                        SwitchCase or ErrorCase of any task of D }
```

No dataflow, no fixpoint — `Fault.Code` is a plain field. The set is *statically
exact*, and where imprecise it is imprecise in the safe direction: a `raise` on
an unreachable task inflates the set, never the reverse.

**Panic codes are excluded, even though panics have codes.** `raises(D)` is the
set a parent may write rules against (R5), and no rule can ever match a panic —
a panicking child is `failed`, so it poisons its ancestors and the parent never
reaches resolution at all. Including panic codes would let R5 bless rules that
can never fire. A panic code is for observers; the raise set is for callers.

**Cost: discoverability.** "What can this process raise?" is no longer answerable
by reading the top of the file. The server closes this by computing `raises(D)`
at registration and exposing it on the definition endpoint and in the generated
schema — a first-class property of a published version that nobody authors
directly.

### 2.4 Parent side — no prefix, no new syntax

```yaml
- id: pay
  action:
    type: child_map
    children:
      charge: { name: charge-card, input: { … }, result_schema: { … } }
  on_error:
    - code: ["card_declined"]
      goto: $offer_alternative
    - code: ["insufficient_funds"]
      goto: $notify_customer
  switch: next
```

Raised codes need no `child.` namespace, because **on a child task there is
nothing else `on_error` can ever see.** The other paths a `child_map` /
`child_list` task can fail on — input validation, definition lookup, spawn,
collect-time output validation — all go straight to `failInstance`
([child.go:170-183](internal/engine/child.go#L170-L183),
[child.go:28](internal/engine/child.go#L28)) and are not routed through
`on_error` at all. So a child task's `on_error` list is, by construction, a list
of raised codes.

They are also lexically distinct from engine codes at a glance: R1 forbids `.`
in a raised code, while every engine code contains one (`http.500`,
`pre.timeout`, `external.timeout`, `output.invalid`).

**Propagation is explicit.** A parent re-raising a child's error is just a `raise`
in an `on_error` rule — and that is what puts the new code into *this* process's
raise set:

```yaml
  on_error:
    - code: ["card_declined"]
      raise:
        code: payment_failed
        message: "payment could not be completed"
```

## 3. Static semantics (registration)

Each rule states its rejection message verbatim.

**R1 — fault shape.** On every `Fault` — under `raise` or `panic` alike —
`Code` matches `^[a-z][a-z0-9_]*$` (no dots; those belong to engine codes) and
`Message` is non-empty.
> `task "charge": raise: "Card-Declined" is not a valid error code (use lower_snake_case, no dots)`
> `task "charge": raise "card_declined": message is required`

**R2 — faults are static.** Neither `Code` nor `Message` contains `{{ }}`. A
computed code would make `raises(D)` uncomputable and `error_code` unqueryable;
a computed message would smuggle data across the boundary.
> `task "charge": raise: code must be a literal, not an expression`
> `task "submit": panic: message must be a literal, not an expression`

**R3 — exactly one terminal clause.** A `SwitchCase` or `ErrorCase` carries
exactly one of `goto`, `raise`, `panic`.
> `task "charge": switch case 0: set exactly one of "goto", "raise", "panic"`

**R4 — child tasks match literally, and do not retry.** On a task whose action is
`child_map` or `child_list`, every `on_error` rule has a non-empty `code` list
containing no `%` or `_` — no catch-all, no patterns. `retries` and `not_reached`
are rejected outright: there is no parent-side retry (D7), and accepting a field
that is silently ignored would be worse than refusing it.
> `task "pay": on_error[0]: a child task requires literal error codes — catch-all and LIKE patterns are not allowed`
> `task "pay": on_error[0]: retries is not supported on a child task; retry inside the child, then raise`
> `task "pay": on_error[0]: not_reached has no meaning on a child task`

**R5 — rule reachability.** On a child task, every code named by an `on_error`
rule is in `⋃ raises(D)` over the task's resolved children (the union across
entries, for `child_map`).
> `task "pay": on_error[1]: no child of this task can raise "card_expired"`

**R6 — a code is a raise code or a panic code, not both.** Within one definition,
no code appears on both a `raise` and a `panic`. Otherwise `error_code` would be
ambiguous for exactly the observers it exists to serve: the same value would
appear on `errored` and `failed` instances of the same process, meaning two
different things.
> `task "submit": panic "timeout": this code is already raised by task "poll"; a code cannot be both raised and panicked`

R5 lands in `validateChildEntry`
([validate_children.go:74](internal/validation/validate_children.go#L74)), which
already resolves the child definition to subset-check its input, so its task list
is in hand.

### 3.1 What R5 does and does not catch

R5 is a **sanity check, not a coverage guarantee.** It runs in one direction
only — from rule to raise set:

| change | caught at registration? |
|---|---|
| rule names a code with a typo (`card_decline`) | **yes** |
| a code is *removed* from a child, orphaning a rule | **yes**, when the parent re-registers against the new version |
| a code is *added* to a child, with no rule for it | **no** — surfaces at runtime |

The third row is the deliberate cost. An unhandled raised code reaches step 2 of
resolution with `rule = nil` and **fails the parent** (§5.2). Version pinning
(`GetDependencyVersion`, [child.go:116](internal/engine/child.go#L116)) bounds the
blast radius: a running parent keeps its pinned child version, so a new code
reaches it only after someone deliberately bumps the dependency and
re-registers — a runtime failure on a new pairing, not a retroactive break of
what is already deployed.

Requiring every raisable code to be handled was considered and rejected: it makes
a shared child process painful to depend on, since any new raise site anywhere in
it would break re-registration for every parent. See D3.

## 4. Matching

**M1 — literal matching on child tasks.** `matchOnError`
([error.go:28](internal/engine/error.go#L28)) gains one clause:

> On a child task, a rule matches **iff its `code` list contains the raised code
> literally.** No LIKE evaluation, no empty-list catch-all.

R4 rejects at registration everything this clause would silently skip, so the two
never disagree. Non-child tasks keep today's `SQLLikeMatch` semantics unchanged
for `http.*` / `pre.*` / `external.timeout` / `script.*` / `output.*`.

This is the mechanical expression of the first corollary in §0: a child error is
handled by naming it, or not at all.

## 5. Operational semantics

### 5.1 Child: raising

A `raise` clause is a sibling of the `GotoEnd` branch in `advance()`
([advance.go:253-262](internal/engine/advance.go#L253-L262)), and of the
`GotoEnd` branch in `handleCallError` for the `on_error` form
([error.go:78-83](internal/engine/error.go#L78-L83)):

```
raise:                            panic:
  status     := errored             status     := failed
  error_code := fault.Code          error_code := fault.Code
  error      := fault.Message       error      := fault.Message
  wake_at    := nil                 wake_at    := nil
```

The two write identical fields and differ only in status — which is the whole
difference, since status is what decides whether ancestors are poisoned.

`panic` is `failInstance` ([error.go:99](internal/engine/error.go#L99)) with an
authored reason and code, so it reaches `FailInstanceAndAncestors` through the
existing `Status == StatusFailed` branch of `saveAndNotify` with no new plumbing.

`raise` is the new path: the terminal write goes through `saveAndNotify`
([advance.go:389](internal/engine/advance.go#L389)) via **`FinishChild`**, not
`FailInstanceAndAncestors`, because a raise is a normal outcome and must not mark
ancestors `failing`.

`StatusErrored` is directly terminal; no draining state is needed, because a
raise happens at a task boundary where this instance's own children have already
collected.

**Neither computes the process `output`.** This matters at registration as well
as at runtime: `addTerminal` ([context.go:83](internal/validation/context.go#L83))
must be called only for `goto: end` cases, never for `raise` or `panic`. A raise
site is *not* a terminal for the purpose of validating the process `output:`
expression — otherwise raising from a task where the outputs `output:` reads are
not yet available would fail registration, which is precisely the situation
every early-exit raise is in.

### 5.2 Parent: resolution

**Precondition:** `P.status = running ∧ P.wait_state = collecting`. A `failing`
or `cancelling` parent short-circuits earlier in `advance()`
([advance.go:139-144](internal/engine/advance.go#L139-L144)) and never resolves.

That precondition has a strong consequence. A `failed` child poisons its
ancestors before the parent can wake (§5.4), and a `cancelled` child implies a
cancel that came down from the root, so the parent is `cancelling`. **A parent
that reaches resolution therefore has a batch of only `completed` and `errored`
children** — the algorithm never has to reason about defects at all:

```
resolve(parent P, task T):
  B := children of batch(P, T.id), ordered by slot
       -- child_list: by _spawn_index;  child_map: by sorted _spawn_child_key
  assert ∀c ∈ B : c.status ∈ {completed, errored}

  E := [ c ∈ B : c.status = errored ]                   -- keeps slot order

  ── 0. happy path ────────────────────────────────────────────────────────
  if E = ∅:
      collect outputs as today → self.result;  continue advance()

  ── 1. match ─────────────────────────────────────────────────────────────
  for c ∈ E:
      c.rule := matchOnError(T, c.error_code)            -- literal; nil if none

  ── 2. route ─────────────────────────────────────────────────────────────
  f := E[0]                                              -- first in slot order
  write $error from f                                    -- §5.3
  match f.rule:
      nil, or no goto/raise → fail P
      goto = "end"          → complete P
      raise                 → raise from P               -- §5.1
      goto = "$id"          → P.task := id;  continue
```

Two steps, no retry, no loop. Ordering is by slot, already established by
`buildListChildOutput` / `buildMapChildOutput`
([collect.go:39-71](internal/engine/collect.go#L39-L71)). Nothing reads a clock
or observes completion order, so resolution is deterministic (**I3**).

Step 2's `rule = nil` branch is the **normal** path for an unhandled raised code,
not an edge case — R5 does not require coverage (§3.1). Its message must name the
code, the child and the slot, so the omission is readable straight off the
instance detail:
> `task "pay": child "charge-card" (slot charge) raised "insufficient_funds"; no on_error rule matches`

Note what this means for composition: **an unhandled raise degrades to a defect.**
The parent fails, which fails fast up its own tree. An error never propagates a
level implicitly — propagation is a `raise` in an `on_error` rule and nothing
else.

### 5.3 What the routed task sees

`$error` (the existing `error_data` slot):

```
{ task:     "fanout",
  code:     "out_of_stock",
  message:  "no stock remaining for this sku",
  slot:     {index: 3},              -- or {key: "charge"}
  siblings: [                        -- every error in the batch, slot order
    {slot: {index: 3}, code: "out_of_stock",    message: "…"},
    {slot: {index: 8}, code: "address_invalid", message: "…"} ] }
```

`slot` costs nothing — the engine already knows which child raised — and it is
what makes a payload-free design workable for fan-outs: the child never says
*which one*, because its identity is structural. `siblings` is the difference
between "something failed" and "6 of 10 failed". Both are identity and code
only; neither carries child data (**I6**).

**The routed task keeps its normal context.** `input`, `config`, and every
`outputs.<id>` from previously completed tasks are readable as usual. The single
thing absent is `outputs.<T.id>` — the failed batch never produced one. That is
the whole of "no partial data": there is no half-populated output, not that the
handler is blindfolded.

Typing follows in `contextSchema`
([infer.go:253-263](internal/validation/infer.go#L253-L263)); *presence* of
`$error` is already computed by the existing mustErr/mayErr dataflow
([context.go:337-400](internal/validation/context.go#L337-L400)) and needs no
change.

### 5.4 Defects fail fast, always

A child that *fails* rather than raises is **not catchable, under any
configuration** — and that holds identically whether the failure was authored
with `panic` or produced by the engine. It calls `FailInstanceAndAncestors`
([db_lifecycle.go:83](internal/db/db_lifecycle.go#L83)) exactly as today:
ancestors go `failing`, the batch is abandoned, the parent settles without ever
resolving.

There is no `child.failed` code, no opt-in flag, no per-spawn stamp. This is the
first corollary of §0 made structural rather than merely discouraged: the only
way to make a failure catchable is to convert it into a raise *inside the child*,
at the specific task where you understand what went wrong —

```yaml
    on_error:
      - code: ["http.503"]
        retries: 3
        raise:
          code: psp_unavailable
          message: "the payment provider is unavailable"
```

— which is exactly the reasoning a blanket catch-and-retry lets you skip. Note
that `retries` here is on a *fetch* task inside the child, where it has always
been legal and where transient failures actually belong.

An unhandled defect in one slot therefore dominates a sibling's tidy raise in
another: the parent goes `failing` and the raise is never routed. Correct — a
fault must not be masked by a business error that happened to occur beside it.

## 6. Invariants

- **I1 — all-or-nothing.** `outputs[T.id]` is written only when every child of
  the batch is `completed`. There is no partially populated batch output.
- **I2 — single observation.** A parent resolves a batch exactly once, when
  every child is terminal — and every one of them is then `completed` or
  `errored` (§5.2).
- **I3 — determinism.** Resolution is a pure function of `(T, slot-ordered
  children)`. No clock, no completion-order dependence, no race.
- **I4 — crash safety.** From I3, re-running resolution over the same rows
  yields the same decision, so a crash between decision and persist is safe and
  a reclaimed parent resumes identically.
- **I5 — caller independence.** A child's terminal status, *and its effect on its
  ancestors*, are fully independent of who spawned it. Nothing about a child's
  termination is parameterised by its parent.
- **I6 — no data crosses.** After an error route, the only child-derived values
  in the parent's context are code, message and slot.

## 7. Data model

The footprint is deliberately small — one column, one status, one `NOT IN` list.

**Migration `NNN_child_errors.up.sql`:**
```sql
ALTER TABLE process_instances ADD COLUMN error_code TEXT;
```
Filterable, not sortable — so no index is required (only sorts must be
index-backed; see `paginate.go`).

### 7.1 `error_code` is the discriminator for every non-success outcome

Once panics carry a code, `error_code` should be populated on *every* terminal
failure, not just authored ones:

| terminal state | `error_code` | source |
|---|---|---|
| `completed` | null | — |
| `errored` | the raised code | `raise` |
| `failed`, authored | the panic code | `panic` |
| `failed`, engine | the engine code — `http.500`, `pre.timeout`, `output.invalid` … | `handleCallError` |

The last row is a small extension beyond `raise`/`panic`, and it is where most of
the operational value is: today an engine failure's code exists only inside the
`error` text, formatted into `task %q: %s: %s`
([error.go:57](internal/engine/error.go#L57),
[error.go:94](internal/engine/error.go#L94)), so it cannot be grouped or alerted
on without parsing prose. `handleCallError` already holds `errCode` — populating
the column is a one-line change that makes *all* failures uniformly queryable.

The two namespaces stay legible side by side because R1 forbids dots in authored
codes and every engine code has one: `bad_credentials` is something a definition
decided, `http.401` is something the engine observed. A filter never has to
disambiguate them, and a reader can tell at a glance which kind of failure they
are looking at.

**Query changes in `queries.sql`:**
```sql
-- CountActiveSiblings: 'errored' is terminal. Without this the parent never wakes.
AND status NOT IN ('completed', 'failed', 'cancelled', 'errored');

-- GetChildrenForTask / instanceColumns: add error_code to the projection.
```

**New context keys:** none.

**Go:**
- `model.Fault`; `SwitchCase.Raise` / `.Panic`; `ErrorCase.Raise` / `.Panic`;
  `model.StatusErrored`.
- `ProcessInstance.ErrorCode`; `InstanceSummary.ErrorCode`.
- `model.ProcessDefinition.Raises()` — the §2.3 scan, used by R5 and the
  definition endpoint. Panics are not included.
- `addTerminal` ([context.go:83](internal/validation/context.go#L83)): fires only
  for `goto: end`, not for `raise` / `panic` (§5.1).
- `RetryProcess` ([db_lifecycle.go:241](internal/db/db_lifecycle.go#L241)): four
  changes, see §11.
- `collectChildOutputs` ([collect.go:26](internal/engine/collect.go#L26)):
  accept `errored` siblings instead of hard-erroring.
- API: `errored` in status filters; instance detail returns `error_code`;
  definition detail returns the derived raise set.

**Untouched:** `internal/schema`, `internal/expression`, `internal/template`, and
every sibling query beyond the one `NOT IN` list.

## 8. Edge cases

| # | case | resolution |
|---|---|---|
| E1 | child raises while parent is `cancelling` | child is `errored`; parent short-circuits to `cancelInstance` and never resolves. Matches the existing precedent that an error outranks a cancellation once retries are gone ([error.go:49-58](internal/engine/error.go#L49-L58)) |
| E2 | parent already `failing` | short-circuits to `settleFailing`; resolution never runs |
| E3 | batch mixes a raise and a defect | defect wins: it poisons ancestors first, so the parent never resolves and the raise is never routed (§5.4) |
| E3b | child `panic`s | identical to any other defect — `failed`, ancestors poisoned, uncatchable. The authored message replaces the engine's generic reason, which is the only observable difference |
| E4 | `child_list` over `[]` | unchanged — no children, continues inline ([child.go:64-72](internal/engine/child.go#L64-L72)) |
| E5 | two slots raise the same code | first in slot order routes; both appear in `siblings` |
| E6 | spawn-time failure (bad input, missing definition) | unchanged — `failInstance`, not routed through `on_error`. This is what makes a child task's `on_error` list purely raised codes (§2.4) |
| E7 | child raises at its first task | fine; no output is computed either way |
| E8 | root process raises | no parent; `UpdateInstance`; API reports `errored` + `error_code` |
| E9 | grandchild raises, child does not handle it | child fails (§5.2 step 2), which fails fast up its own tree. An error never crosses two levels implicitly |
| E10 | self-referencing (recursive) child | R5 checks `D`'s rules against `raises(D)` itself. No fixpoint: `raises` is a syntactic scan, so it terminates |
| E11 | `child_map` where only some children raise | R5 takes the union of `raises` over all entries of the task |
| E12 | parent crashes mid-resolution | safe by I4 |
| E13 | handler routes back into the main flow | legal, but `outputs[T.id]` is absent — reading it is a registration error via the existing reachability analysis |
| E14 | a `raise` on an unreachable task | inflates `raises(D)`; R5 permits rules for a code that cannot occur. Conservative in the safe direction |
| E15 | child gains a new raise site after the parent registered | not caught (§3.1); parent fails at runtime when it occurs. Bounded by version pinning |

## 9. Locked decisions

- **D1 — no `child.` prefix.** A child task's `on_error` can only ever see raised
  codes, because every other failure path on those actions goes straight to
  `failInstance` (E6). Raised codes are also lexically distinct from engine codes
  (R1 forbids dots; every engine code has one).
- **D2 — `siblings` is in scope.** Argued as information, not data: "6 of 10
  failed" is a branching input, not child state. It is the one place the "signal,
  not value" line is blurry.
- **D3 — reachability check only; no exhaustiveness.** R5 verifies that every
  rule *can* fire; nothing verifies that every raisable code *has* a rule.
  Requiring the latter makes a shared child painful to depend on. Accepted cost:
  §3.1's third row. **Note the direction:** adding exhaustiveness later would
  break existing definitions, so this is the harder of the two to reverse.
- **D4 — `errored` is a distinct status.** Not `completed` (every filter,
  dashboard and alert keys on `Status`; folding a third outcome in would
  mislabel all of them) and not `failed` (which means defect and poisons
  ancestors). It is **not retryable**, and is treated as settled work at every
  depth (§11.1, §11.2): a raise is a declared outcome, and retry — which can only
  re-run the *deciding* task, never the upstream state that drove the decision —
  has nothing to offer it. `failed` stays retryable whether the fault was
  detected by the engine or authored with `panic`.
- **D5 — defects are never catchable.** No `child.failed`, no opt-in. Making a
  failure catchable requires converting it to a raise inside the child, at the
  task that understands it (§5.4). An authored `panic` is a defect like any
  other — authoring it grants no special status.
- **D6 — `panic` carries a code, for classification not branching.** An earlier
  draft gave `panic` a message only, reasoning that a code exists to be branched
  on and nothing can branch on a panic. That conflated two audiences. Parents
  branch; the API's consumers *classify* — alerting, dashboards, "how many
  instances died of this, and did it start after Tuesday's deploy". `error_code`
  is a filterable column and free text is useless for that, so panics carry the
  same `Fault` as raises.
  What remains asymmetric is reachability, not shape: panic codes are excluded
  from `raises(D)` (§2.3), because no `on_error` rule can ever match one and R5
  would otherwise bless rules that can never fire. R6 keeps a code from being
  both, so `error_code` means one thing per process.
- **D7 — no parent-side retry.** By §0 a raised error describes a *settled*
  condition: the child already exhausted whatever transient retries made sense
  before giving up, so re-spawning it mostly reproduces the same error. `retries`
  is therefore rejected on child tasks (R4) rather than silently ignored.
  This removes the only part of the design that would have touched the
  concurrency-sensitive sibling queries — a per-slot attempt dimension
  (`_spawn_attempt`, live-attempt selection, `CountActiveSiblings`), which sits
  inside the lock-ordering discipline that prevents deadlocks
  ([db_lifecycle.go:27-30](internal/db/db_lifecycle.go#L27-L30)).
  **Adding it later is purely additive** — no definition written under R4 can
  change meaning when it lands — so the decision is cheap to revisit if a real
  use case appears. §10.1 records what the coarse workaround looks like in the
  meantime, and doing it repeatedly is the signal to build the real thing.

## 10. Phasing

1. **Raise and panic.** `Fault` type, `SwitchCase` / `ErrorCase` gaining
   `.Raise` and `.Panic`, `Raises()`, `StatusErrored`, R1–R3 and R6, the
   `addTerminal` fix, migration, `error_code` populated on every terminal failure
   (§7.1), `CountActiveSiblings`. A raising child still fails its parent — but a
   *root* process can already report a typed failure, and `panic` is complete on
   its own from day one, since nothing ever reacts to it. Includes all of §11 —
   `RetryProcess` must understand `errored` from the moment the status exists, or
   §11.4 parks revived parents in `waiting` forever.
2. **Catch, single child.** M1, R4/R5, `saveAndNotify` routing,
   `collectChildOutputs` accepting `errored`. Complete for a one-entry
   `child_map`.
3. **Batch resolution.** §5.2 over a multi-child batch, `$error.slot` /
   `.siblings`. Complete for fan-outs.

### 10.1 Re-running a batch without retry

A parent that genuinely needs to re-run a batch can do it with a `goto` back to
the spawning task: the error route clears `wait_state`, so re-entering the task
spawns a fresh batch.

```yaml
- id: pay
  action: { type: child_map, children: { … } }
  on_error:
    - code: ["psp_unavailable"]
      goto: $pay          # re-spawn the whole batch
```

This is deliberately coarse — it re-spawns **every** slot, discarding the
successful ones — so it is wrong for `only_once` children and wasteful for
fan-outs. Nothing bounds the loop either, so the definition must carry its own
counter (a task output incremented per pass, as in the polling example).

Those limitations are exactly what per-slot retry (D7) would fix. If definitions
start reaching for this pattern, that is the evidence to build it.

## 11. The `retry` command

`RetryProcess` ([db_lifecycle.go:241](internal/db/db_lifecycle.go#L241)) revives a
settled tree: it locks root + descendants, walks top-down, and restarts the
interrupted path while keeping finished work.

The net change is small — the root gate is untouched — but two of the four points
below are correctness fixes, not adjustments.

**The unit of retry, settled.** A question this design could have left open is
whether retrying a parent whose child raised should re-run the *child* or
re-run the *parent*. It re-runs the parent, always, and never the child — and
that falls out of two decisions already made rather than needing a rule of its
own: per-slot child retry was removed (D7), and a raised child is settled work
that `revive` keeps (§11.2).

The general form is the same fault/outcome split as everywhere else:

- a child that **raised** is never restarted — it concluded;
- a child that **faulted** is restarted, as part of reviving the interrupted
  path, because the parent never got an answer from it.

So there is exactly one unit of retry — a process — and exactly one way to
restart work: name the root and let `revive` walk down to whatever was actually
interrupted.

### 11.1 What retry actually does, and what that implies

Retry re-runs **the task the instance is sitting on**. It has no rewind: it
cannot restart an earlier task, and it cannot alter persisted context. That
single fact decides everything below.

For a **fault**, retry's model fits: the task's action failed, the cause is *at*
that task, and re-running re-attempts exactly the thing that broke.

For a **raise**, it usually does not. `inst.Task` is the task whose switch made
the decision, but the state that *drove* the decision is often upstream:

```yaml
- id: check                       # raise reads a PRIOR task's output
  switch:
    - case: "outputs.validate.ok == false"
      raise: { code: invalid_order, message: "the order failed validation" }
    - goto: end
```

Re-running `check` re-evaluates the same expression against the same stored
`outputs.validate`, which retry cannot have changed. It re-raises, always. For a
switch-only task this is *provable*: there is no action, so nothing but persisted
context feeds the decision.

And when the raising task *does* have an action, retry is worse than futile — the
action already **succeeded** (the switch ran on its result), so re-running
re-executes a side effect that worked, and then re-raises on the same upstream
state.

So the line is not authored-versus-engine. It is **fault versus outcome** — and
the status already encodes it exactly:

| terminal state | meaning | retryable |
|---|---|---|
| `completed` | finished | no — nothing to recover |
| `errored` (raise) | an anticipated condition concluded the process | **no** |
| `failed` (panic) | the author detected a fault the engine could not | yes |
| `failed` (engine) | the engine detected a fault | yes |
| `cancelled` | stopped by an operator | yes |

**So the root gate does not change at all:**

```go
if status != model.StatusFailed && status != model.StatusCancelled {
    return fmt.Errorf("process is not retryable (status: %s)", status)
}
```

An `errored` root falls through it and is rejected — correctly, and with no new
code. The message should say why, since "not retryable" alone invites a bug
report:

> `process concluded with error "insufficient_funds" (status: errored); a raised error is a declared outcome, not a fault — start a new instance, or publish a new version if the outcome should be handled differently`

**Panics stay retryable**, and that is not an inconsistency. `panic` chose the
`failed` status precisely because it means *this is a fault* — the author is
reporting something broken that the engine could not see, like a `200` with an
error body. Faults are what retry is for: fix the cause outside the process,
re-enter. An author who wanted "this is a settled outcome, do not re-run" has a
clause for that, and it is `raise`.

The residual case — a panic whose condition also reads stale upstream output — is
real, and retry is futile there too. But that is retry-has-no-rewind biting, a
property it already has for any failure whose cause is upstream. It is not
something `panic` introduces, and it does not justify a discriminator column to
separate authored from engine failures.

#### Why not gate on "the panicking task has an action"

A tempting refinement is to allow retry only when the current task has an
action, on the grounds that a switch-only task re-evaluates persisted state and
must decide identically. Rejected, for two reasons.

**The proxy is wrong: `config` is live.** It is re-resolved from the OS
environment every tick, never persisted
([advance.go:90-96](internal/engine/advance.go#L90-L96)), and `buildEnv` always
puts it in scope ([evaluator.go:44](internal/engine/evaluator.go#L44)). So a
switch-only task *can* decide differently on a retry:

```yaml
- id: guard
  switch:
    - case: "config.psp_enabled == false"
      panic: { code: psp_disabled, message: "the PSP integration is turned off" }
    - goto: next
```

Flip the env var, retry, and it proceeds — precisely the fix-outside-and-re-enter
case retry exists for. A correct version of the rule would have to ask whether
the deciding expression reads `config`, which means extending
`expression.Roots` and gating an operator-facing API on a static analysis that
must never be subtly wrong.

**Futility is not a gating criterion anywhere else.** A task whose response fails
its `result_schema` produces `output.invalid`; re-running fetches the same
response and fails identically, because the fix is a new definition version. That
retry is exactly as futile and is allowed today. Refusing only panics would be an
arbitrary carve-out for the one case we happened to examine.

The cost of permitting a futile retry is one claim and one advance, after which
the operator sees the identical failure and knows more than before. The cost of
forbidding it is an API whose behaviour depends on a definition detail the
operator may not have in front of them. If the concern is worth addressing, the
proportionate lever is *information* — have the retry response name the task that
will re-run and whether it has an action — not a refusal.

### 11.2 `errored` is settled work — keep it, do not revive it

`revive` switches on status ([db_lifecycle.go:341-348](internal/db/db_lifecycle.go#L341-L348)):
`completed` returns (finished work is kept); `running`/`failing`/`cancelling`
return (defensive); **everything else is revived**. So `errored` currently falls
through to revival by omission — and by §11.1 that is wrong. It belongs with
`completed`:

```go
case model.StatusCompleted, model.StatusErrored:
    return nil // settled work is kept — see §11.1
```

The comment below the switch, *"node is failed or cancelled"*, is then accurate
again.

This keeps the status meaning one thing at every depth: **`errored` is concluded,
whether it is the root or a leaf.** An errored child was not interrupted; it
finished, by design, with a declared outcome.

**A limitation worth stating rather than hiding.** The only case where this
matters is a parent that failed *at resolution* because no rule matched a child's
raised code. Reviving that parent puts it back on the spawn task with a settled
batch, so it re-resolves, finds the same unmatched code, and fails identically.
Retry cannot help there — and could not have helped by reviving the child either,
since the missing `on_error` rule lives in a **version-pinned definition** that a
retry does not re-read. The real fix is a new parent version and a new instance.
Better to say so than to offer a retry that quietly changes nothing.

The scoping question this raises — *"what about an errored child whose error the
parent already handled and routed past?"* — is answered by the existing
structure either way. `revive` only looks at `children[node.ID][node.Task]`, the
batch of the node's **current** task; once resolution routes on an error,
`inst.Task` becomes the goto target and that batch is behind the node. Worth a
comment, because it is not obvious and a refactor could break it silently.

### 11.3 Clear `error_code` on revive

`revive` clears `Error`, and `UpdateInstance` writes `Error: ""`
([db_lifecycle.go:426](internal/db/db_lifecycle.go#L426)). `error_code` must be
cleared alongside it.

This is easy to miss and its failure mode is quiet: a revived instance that later
completes would keep a stale `error_code`, so §7.1's filter would report a
successful process as having died of `card_declined`. It corrupts precisely the
column the code exists to serve.

**Open question — the `$error` context slot.** Revive passes `ErrorData:
raw.ErrorData` through verbatim, so `$error` survives while the `error` column is
cleared. That inconsistency predates this design and is mostly harmless (the
mustErr/mayErr dataflow means a stale `$error` is only readable where the
analysis says one may be present), but it is now reachable one more way: a batch
error route writes `$error`, and reviving that parent keeps it. Worth deciding
deliberately rather than inheriting.

### 11.4 The `anyActive` reconstruction — a real bug if missed

```go
for _, k := range kids {
    revive(k)
    if k.Status != Completed && k.Status != Failed && k.Status != Cancelled {
        anyActive = true
    }
}
```
([db_lifecycle.go:358-365](internal/db/db_lifecycle.go#L358-L365))

`revive(k)` runs *first*, so this asks "after revival, is anything running?" and
reconstructs `waiting` vs `collecting` from the answer.

**With §11.2 this is a live bug, not a latent one.** Errored children are *not*
revived, so they stay `errored` — a status absent from all three comparisons.
The condition is true, `anyActive` is set, and the parent is parked in `waiting`
forever, waiting on a child that is already terminal. The instance is
unrecoverable and nothing logs why.

`errored` must be added to the list:

```go
if k.Status != Completed && k.Status != Errored &&
   k.Status != Failed && k.Status != Cancelled {
    anyActive = true
}
```

This is the one change in §11 that is not optional and not cosmetic.

### 11.5 Naming — settle before the `cancel → pause` rename

`ROADMAP.md` says *"Retry only for errored processes."* Written before this
design, "errored" there meant *faulted* — what this spec calls `failed`. With
`errored` now a real status meaning the opposite, that line reads as the
inverse of its intent, and §11.1 makes it wrong twice over (retry accepts
`failed`, `cancelled` **and** `errored`).

Worth resolving together with the roadmap's other rename (`cancel` → `pause`,
then `resume`), since both are about making the lifecycle verbs say what they do.
