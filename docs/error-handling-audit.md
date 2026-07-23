# Error handling: audit and plan

Status: **audit, nothing implemented (2026-07-23).** Addresses `ROADMAP.md` →
"Go + REST API error handling". This is a survey of the error paths as they
stand, not a description of intended behaviour — every issue below is present in
the code at the time of writing.

The finding in one line: **the workflow error model is good and the Go plumbing
under it is not.** Those are two different systems that happen to share the word
"error", and only the first one has been designed.

## What is already right — do not "fix" these

The domain model is deliberate and should be left alone. Listed so that a
cleanup pass does not flatten it in the name of consistency:

- **[errcode](../internal/errcode/errcode.go)** is the single source of truth for
  engine-produced codes, with no genroc dependencies so every layer can import it
  without a cycle. Codes are namespaced, and the namespace carries a semantic
  guarantee: `errcode.NotReached` (`"pre."`) means the request never left, which
  is exactly what makes a retry safe for an `only_once` task
  ([isRetryAllowed](../internal/engine/error.go#L21)). That is a property the
  taxonomy *encodes*, not a naming convention.
- **[advanceOutcome](../internal/engine/advance.go#L22)** is a sum type. The
  failure path (`failInstance`) returns the same type as the success path, so
  errors are values in the normal flow rather than a second control channel.
- **`failInstance(inst, code, reason)`** takes the code positionally, so no
  failure path can leave `error_code` empty. Policy enforced by signature.
- **[ClassifyGoError](../internal/transport/transport.go#L157)** uses
  `errors.As` into `*net.OpError` to separate a dial timeout from a response
  timeout. This is the one place in the codebase that inspects an error properly,
  and it is the place where it matters most.
- **[the expression parser](../internal/expression/syntax/parser.go#L39)** uses
  panic/recover internally and **re-panics** on any value that is not a
  `parseError`, instead of swallowing unrelated bugs.

Nothing below asks to change any of that. The complaint is that this care stops
at the edge of the engine.

---

# Part 1 — REST API

## A1. Every error is `400 Bad Request`

The whole HTTP surface has exactly **two** status writes: a `404` for an unknown
spec route ([server.go:78](../internal/api/server.go#L78)), and a blanket `400`
for everything else ([server.go:153-161](../internal/api/server.go#L153-L161)):

```go
func writeReply(w http.ResponseWriter, r Reply) {
	w.Header().Set("Content-Type", "application/json")
	if !r.OK {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": r.Error})
	}
	...
}
```

So across all 28 registered actions: a database outage is a `400`. A missing
instance ID is a `400`. A malformed payload is a `400`. An unknown action name is
a `400`. A version conflict on apply is a `400`.

This is the single worst thing in the error handling, because it is the only part
clients can see. Consequences that are already real rather than hypothetical:

- **A client cannot distinguish "retry this" from "never retry this".** A `500`
  from a dropped DB connection and a `400` from a typo'd field are the same
  response. Any retry logic built against this API is guessing.
- **Monitoring cannot separate operator error from server failure.** A `4xx` rate
  is meaningless when it contains both.
- **It contradicts the engine.** The engine spent real design effort on machine-
  readable codes so `on_error` can route on them; the API then hands clients a
  single status and a prose string.

## A2. The error code never reaches the client

[Reply](../internal/api/handlers.go#L43) carries `Error string`, and
[errReply](../internal/api/handlers.go#L65) is the only constructor:

```go
func errReply(err error) Reply { return Reply{OK: false, Error: err.Error()} }
```

`err.Error()` is called at the *action* layer, so by the time `writeReply` runs,
the error value is gone — there is nothing left to inspect even if `writeReply`
wanted to map it to a status. This is why A1 cannot be fixed in `writeReply`
alone; the type has to carry the classification.

Note this also affects TCP and UDS, which encode `Reply` directly
([handleConn](../internal/api/server.go#L137)). A code field on `Reply` benefits
all three transports; a status code is HTTP's rendering of it.

## A3. Structured validation errors are flattened to a string

[fmtValidationErr](../internal/model/validate.go#L488) receives
`validator.ValidationErrors` — one entry per failed field, each with the field
name, the failed tag and its parameter — and joins them:

```go
return fmt.Errorf("%s", strings.Join(msgs, "; "))
```

The message is good for a human, but a client submitting a definition cannot
learn *which field* failed without parsing English. For an API whose main job is
accepting user-authored process definitions, per-field errors are close to a
requirement, and the underlying library already produces them.

## A4. `okReply` reports success when marshalling failed

[handlers.go:60-63](../internal/api/handlers.go#L60-L63):

```go
func okReply(v interface{}) Reply {
	data, _ := json.Marshal(v)
	return Reply{OK: true, Data: data}
}
```

A marshal failure yields `Reply{OK: true, Data: nil}` — HTTP `200` with an empty
body. Rare (most payloads are plain structs) but silent, and silent is the
problem: the failure mode is a client that believes it received an empty result.

## A5. `decodeOptionalBody` discards decode errors

[handlers.go:81-87](../internal/api/handlers.go#L81-L87) drops the error from
`numeric.Decode`, so a malformed optional body is indistinguishable from an
absent one and silently becomes the zero value. Optional-ness is about *presence*
— it should not also mean "unparseable is fine". Contrast `decodeBody`, which
does it correctly.

---

# Part 2 — Go-level plumbing

## G1. `%w` wrapping is decorative

Counts across non-generated code:

| | |
|---|---|
| `fmt.Errorf` calls | 395 |
| …of which wrap with `%w` | 157 (~40%) |
| `errors.Is` / `errors.As` sites (non-test) | **5** |
| `Unwrap() error` methods on project types | **0** |

The chain is built and never walked. Go 1.13 wrapping has been adopted
syntactically but not semantically: `%w` here produces a nicer message string,
not an inspectable value. Either is a defensible choice — but the current state
pays the cost of the first and gets the benefit of neither.

The practical consequence is that no caller can react to *what* went wrong, only
report it. That is why A1 is hard to fix incrementally: there are no error values
to classify.

## G2. `sql.ErrNoRows` is compared with `==`

Ten sites compare directly, one uses `errors.Is`:

| Site | Form |
|---|---|
| [db_objects.go:149](../internal/db/db_objects.go#L149) | `errors.Is` ✅ |
| [db_signals.go:35](../internal/db/db_signals.go#L35), [:121](../internal/db/db_signals.go#L121) | `case sql.ErrNoRows:` |
| [db_signals.go:42](../internal/db/db_signals.go#L42) | `popErr != sql.ErrNoRows` |
| [db_instances.go:446](../internal/db/db_instances.go#L446) | `err == sql.ErrNoRows` |
| [db_lifecycle.go:40](../internal/db/db_lifecycle.go#L40), [:139](../internal/db/db_lifecycle.go#L139) | `err != sql.ErrNoRows` |
| [db_registry.go:107](../internal/db/db_registry.go#L107), [:192](../internal/db/db_registry.go#L192), [:288](../internal/db/db_registry.go#L288) | `err == sql.ErrNoRows` |
| [db_external.go:67](../internal/db/db_external.go#L67) | `err == sql.ErrNoRows` |

This is **not a live bug**: `sql.Row.Scan` returns the sentinel unwrapped, from
`database/sql` itself rather than the driver, so `==` holds for both sqlite3 and
pgx today. It is on the list because it is inconsistent with the one correct
site, and because it breaks the moment any wrapper is introduced between the
query and the caller — which is exactly what a `db`-layer typed-error pass (G5)
would introduce.

## G3. One genuinely fragile string match

[server.go:127](../internal/api/server.go#L127):

```go
if strings.Contains(err.Error(), "use of closed network connection") {
```

This is the normal shutdown path of `acceptLoop` — the listener is closed by the
context goroutine and `Accept` fails. It should be `errors.Is(err, net.ErrClosed)`
(available since Go 1.16). As written it depends on the text of a stdlib error
string, and a mismatch turns a clean shutdown into a logged error plus a hot
`continue` loop.

## G4. No panic barrier around advance goroutines

[dispatch](../internal/engine/engine.go#L277) spawns the advance without a
`recover()`:

```go
go func() {
	defer wg.Done()
	defer func() { <-e.sem }()
	if err := e.runAdvance(ctx, inst); err != nil { ... }
}()
```

A nil-map write or an index-out-of-range anywhere under `advance` — expression
evaluation, JSON handling, collect — takes down the whole worker process, with
instances leased.

This may well be the right behaviour: leases expire and another worker picks the
instances up, and a panic means engine state is suspect, which is the same
reasoning that makes [OverwhelmError](../internal/engine/engine.go#L42) stop the
pump. **The problem is that it is undocumented.** In a codebase where the pause
landing rules and the recursive-CTE lock order both carry paragraph-length
rationale, an unguarded goroutine reads as an oversight rather than a decision.

Resolve it either way, but resolve it explicitly — see **D2**.

## G5. `errcode` codes are bare strings

```go
const HTTPTimeout = "http.timeout"
```

Every consumer (50 references) takes and returns `string`, so an arbitrary string
can flow into `failInstance` where a code is expected, and `IsNotReached` is a
free function doing a prefix test rather than a method on the value.

Strings are **correct at the boundaries** — these codes are persisted to
`error_code` and matched against user-authored `on_error` patterns from YAML. The
gap is only that there is no typed representation *inside* the engine that
serialises to them. `type Code string` costs nothing, keeps every existing
literal valid, and makes `code.IsNotReached()` a method.

## G6. Log-and-continue is the only recovery strategy

Twenty logging calls in non-test code, and the engine's background loops
(`leaseRenewer`, log pruning, object expiry) all log the error and continue. That
is right for a poller — the next tick retries. But there is no escalation: a lease
renewer that has failed every attempt for ten minutes is indistinguishable from
one that failed once, and the worker keeps claiming work it can no longer hold.
Low priority; noted so it is not mistaken for an oversight when G1–G5 are done.

---

# Plan

Ordered by value per unit of work. Phase 1 is the one that matters.

## Phase 1 — API status codes and codes on the wire (fixes A1, A2)

Add a code to `Reply` and a typed API error; map code → status in `writeReply`.

```go
// internal/api/apierr.go
type Error struct {
	Code    string // "not_found" | "invalid" | "conflict" | "internal" | ...
	Message string
	Err     error  // wrapped cause, if any
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }
```

`Reply` gains `Code string` (transport-neutral — TCP and UDS clients get it too);
`errReply` does `errors.As` to extract it and defaults to `internal` when the
error is not an `*apierr.Error`. `writeReply` maps the code to a status through
one table. Default for an unclassified error is **`500`, not `400`** — an error
nobody bothered to classify is a server problem until proven otherwise, and that
default is what makes the migration self-driving: anything still returning `500`
in a test is an unclassified path.

Then classify the handlers, which is the bulk of the work but is mechanical and
can land incrementally, action by action, since unclassified paths keep working.

**Migration cost is near zero**: the test suite asserts on a `400` in exactly two
places, both in
[map_expression_test.ts](../tests/integration/map_expression_test.ts#L115) (the
`toBe(500)` in `logs_test.ts` is a *workflow's* HTTP call status in a log entry,
not an API response). `genctl` treats any `>= 400` uniformly
([http.go:26](../cmd/genctl/http.go#L26), [:149](../cmd/genctl/http.go#L149)), so
the CLI needs no change at all.

## Phase 2 — mechanical correctness (fixes G2, G3, A4, A5)

`errors.Is` for the ten `sql.ErrNoRows` sites; `errors.Is(err, net.ErrClosed)`
for the accept loop; propagate the marshal error in `okReply`; decide and
document `decodeOptionalBody`. Independent of Phase 1, no design decisions, one
sitting.

## Phase 3 — typed errors where they pay (fixes G1, G5)

Not a blanket conversion — 395 `fmt.Errorf` sites do not all need to become
values, and most are fine as messages. Convert where a caller would actually
branch:

- `db`: a not-found sentinel, so handlers stop re-deriving it from `ErrNoRows`
  and Phase 1 can map it to `404` without a per-call-site decision.
- `db`: a conflict/constraint error for version and channel clashes → `409`.
- `validation` / `model`: a structured error carrying per-field detail (A3).
- `errcode`: `type Code string` (G5).

## Phase 4 — panic barrier decision (G4)

Whichever way **D2** resolves, land it as code plus a comment, not as silence.

---

# Open decisions

- **D1 — does the error code become part of the API contract?** Phase 1 puts a
  string code in every error reply. Documenting it in the OpenAPI spec makes it
  stable and therefore expensive to change; leaving it undocumented makes it a
  debugging aid clients should not key on. *Recommendation: document it.* The
  whole point is machine-readable classification, and an undocumented code that
  clients use anyway is the worst of both.

- **D2 — recover in advance goroutines, or keep fail-fast?** Recovering keeps one
  bad definition from killing a worker that is advancing dozens of healthy
  instances; failing fast refuses to continue with suspect state, consistent with
  `OverwhelmError`. *Recommendation: recover, then fail the instance with a new
  `engine.panic` code, and let the process live.* A panic under `advance` is
  almost always attributable to the definition being advanced, not to global
  state — and an instance failed with a code is far more debuggable than a
  restarted worker. Fail-fast is the right default when the blast radius is
  unknown; here it is known and narrow.

- **D3 — should `decodeOptionalBody` reject a malformed body?** Rejecting is
  more correct; tolerating may be load-bearing for some client that sends `""`
  or `null`. *Recommendation: reject, and find out.* If a client breaks, that
  client was sending garbage and silently getting defaults.
