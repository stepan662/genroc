# The `unknown` type: opaque results, narrowed at the boundary

Status: **DRAFT / proposal, 2026-07-21. Not implemented.** Captures a concept
worked out in discussion; details left open are marked *(open)*. Engine facts are
cited to `file:line` so the draft stays honest about what exists vs. what is new.

> A more ambitious version of this idea — passing schemas as values for true
> generic processes with call-site specialization — was considered and
> **deliberately dropped** as not worth its complexity. It is preserved for the
> record in the appendix "Not planned: schema-valued generics". This document
> describes the version we intend to build.

## The idea in one line

Give the type system an `unknown` type: a value a process **handles but does not
inspect**. Anyone who wants to *use* it must validate (narrow) it first — exactly
the way you already have to declare a `result_schema` to read a `fetch` response.

`fetch` is already an "opaque source you must type at the boundary". This lets a
**child process** play the same role. It is not a new concept so much as making
the existing one uniform across `fetch` and processes.

## Motivation

Today `result_schema` must be known statically: it types `self.result` for
downstream expressions, so it has to be a literal on the action. That means a
process cannot hand back data whose shape it doesn't itself know — every result
must be fully typed at the point it is produced.

Often a process legitimately doesn't care what's inside a payload — it fetches it,
carries it, and returns it. Forcing it to declare the shape is both awkward and
sometimes impossible (the shape is the *caller's* concern, not the process's).
`unknown` lets the process stay agnostic and pushes the "what shape is this?"
decision to whoever actually reads the value.

## What `unknown` is — the `{}` top type

Reuse the engine's existing **empty-node top type `{}`**. It already has the three
behaviors we need (this is `unknown`, emphatically **not** `any`):

- **reads rejected** — `self.result.foo` errors ("schema has no properties",
  `internal/schema/navigate.go:159-162`). A black box.
- **`{} ⊄ T`** for any typed `T` (`internal/schema/subset.go:56-61`) — cannot flow
  into a typed slot without narrowing.
- **`X ⊆ {}`** — assignable into a schema-free slot (e.g. `output`).

So an `unknown` value can be **exported or nested in a known structure**
(`{ data: self.result }`) and nothing else. Runtime `conform` against the real
schema, when narrowing happens, enforces the actual shape.

### One change to how an untyped result is represented

Today an action with no `result_schema` produces a result that is *omitted* from
the inference context (`typed=false`, `internal/validation/infer.go:289-291,
359-361`) — which is why it currently cannot even be exported. **Change:** set the
slot to `{}` instead of dropping it, and let `output` carry `{}`.

That single change turns "absent" into "unknown": still unreadable, still not a
subset of any typed slot, but now forwardable.

## Narrowing — the one load-bearing rule

An `unknown` enters the typed world only by being **narrowed** with a concrete
schema, and the narrowing is **runtime-checked**. The narrowing point already
exists in the syntax: the **`result_schema` on the action that produced the
value.** For a child, the parent writes `result_schema` on the child action; the
child's actual output is conformed against it at collect time (`_spawn_result_schema`,
`internal/engine/child.go:200-203` → `internal/engine/collect.go:249-256`), so the
narrowing is sound.

The **one rule to add** is: allow a `result_schema` to narrow a `{}` result — i.e.
permit `{} → T` **through a `result_schema` only** (runtime-conformed), while
keeping `{} ⊄ T` everywhere else. Concretely that is a small relaxation in the
subset check plus the `conform` you already run.

Everything else in this design is reuse. This rule is the whole build.

## Three ways a parent types a child result

A child action's result-type slot has **three modes**, trading coupling for safety
and ergonomics. This is the full story of the slot; `unknown` is one corner of it.

| Mode | Syntax *(open)* | `self.result` | Coupling | On a version bump |
|---|---|---|---|---|
| **Pin** (safeguard) | explicit schema | that schema | decoupled — parent validates without the child | `childOutput ⊆ schema` checked; drift **fails loudly** |
| **Infer** (inherit) | `infer` marker | the child's computed output | **coupled** — child must be defined | auto-adopts the new output; fails only where a changed field is *used* |
| **Unknown** (opaque) | omitted | `{}` | decoupled | n/a — opaque; the consumer narrows |

- **Pin** is today's behaviour, reframed as a *safeguard*: `childOutput ⊆
  resultSchema` (`internal/validation/validate_children.go:230`). The parent
  validates standalone, and if the child's output drifts the subset check fails.
  This is the "explicit type annotation at a public boundary" — you restate the
  return type as a stability gate.
- **Infer** copies the child's *computed* output (`Generate(child)`,
  `validate_children.go:213`) into the slot — `self.result` becomes the child's
  output verbatim, no subset check. This is "import a function; don't re-state its
  return type", and it is what makes child-processes-as-functions pleasant. It
  **tightly couples** parent to child: the child must be defined at parent-validation
  time or it is a hard error. Version pinning is what makes it safe — a pinned
  version is immutable, so there is no drift *within* a version; bumping the pin
  re-inherits the new output and re-checks the parent's usage.
- **Unknown** (the rest of this document): omit → `{}`, opaque, consumer narrows.

**Infer is a larger build than the `unknown` core.** It makes output inference
**recursive across process boundaries** — `Generate` today "infers the child's
output from its own tasks … no getter, so it does not recurse across the tree"
(`validate_children.go:210-212`). So Infer needs: cross-process output resolution;
cycle handling for mutually-recursive processes (reuse the collapse-or-keep /
productivity machinery, now at process granularity); memoization per pinned
`(process, version)`; and a registration-ordering rule (child before parent, or
forward-reference revalidation). This is the ergonomic linchpin for the
[custom-tasks](custom-tasks.md) vision, and its main cost.

**Infer composes with unknown:** a child whose output is `unknown`, inherited via
Infer, yields `unknown` upward; pinning a concrete schema onto an unknown-output
child is exactly the `{} → T` narrowing above.

## How a value moves

An `unknown` value has exactly two legal moves — mirroring TypeScript's `unknown`:

1. **Into a schema-free / `unknown` slot** directly (`{} ⊆ {}`) — pure forwarding;
   nobody downstream can read it either.
2. **Into a typed input only after narrowing** it with a `result_schema` at the
   producing action.

Passing an `unknown` straight into a typed `input` is correctly **rejected**
(`{} ⊄ T`) — the same refusal TypeScript makes when you hand an `unknown` to a
function expecting a concrete type.

## `unknown` is per-field: forward vs. act-on

The rule is per-field, not per-process: **data a process reads to make its own
decisions must stay typed; only data it forwards untouched can be `unknown`.** Most
real processes are a mix.

The poller (`examples/polling-task/`) is the canonical mix:

- `job_id` / `status` — the poller reads these to drive its loop. **Typed**,
  validated where they're read. They cannot be `unknown` or the child couldn't
  poll.
- the final `answer` payload — the child never inspects it, just returns it.
  **This** is the part that becomes `unknown` and is validated when the *parent*
  reads the child result.

So the poller's only behavioral change vs. today: the answer payload is validated
**when the parent reads the child result**, not right after the fetch. Same
runtime guarantee, different clock.

## Consequence: validation is lazy, not eager

Because the check moves from *at the source* to *at consumption*:

- A malformed payload flows through the child and fails at the **parent
  boundary** — later, further from the cause, and **outside the child's own
  `on_error`/retry scope**. Today a `fetch` `result_schema` violation fails the
  fetch task itself, where the child can retry it. That error locality shifts.
- An `unknown` that no one ever narrows is **never validated at all**. Harmless
  (you couldn't read it without narrowing), but lazy-by-design.
- Multiple consumers may each narrow the same `unknown` to **different** schemas,
  each runtime-checked independently. That's a feature.

None of these hurt a stable poller; they're the things to keep in mind before
reaching for `unknown` on data whose shape you don't trust.

## Ledger

**The build:**
- Represent an untyped result as `{}` (not omitted); let `output` carry `{}`.
- Allow `result_schema` to narrow `{}` → `T`, runtime-conformed (`{} → T` only
  through a `result_schema`).

**Reused as-is:**
- `{}` top type (reads rejected, `{} ⊄ T`, `X ⊆ {}`).
- `result_schema` as the narrowing point + its runtime `conform`.
- Everything in the recursion / inference machinery — untouched.

**Trap to avoid:**
- Do **not** represent `unknown` as a dangling `$ref`. A `$ref` to a missing def
  is a hard error on any touch (`navigate.go:276-277`), and an unresolved ref in
  `super` position of `IsSubset` is silently treated as top (`derefSubset`,
  `subset.go:273-285`) — unsound. `{}` is the designed top type; use it.

## Open questions (settle later)

- Surface syntax for the three modes: proposal is **omit** → unknown, **explicit
  schema** → pin, **`infer` marker** → inherit. Exact spelling open — and whether
  "omit" should really mean unknown or stay an error so "I meant unknown" is
  distinguishable from "I forgot". *(open)*
- Infer mode's cross-process inference: cycle handling across mutually-recursive
  processes, `(process, version)` memoization, and registration ordering (child
  before parent). *(open — the main cost of Infer)*
- Author-time ergonomics: when a typed slot rejects an `unknown`, the error should
  point the user at "narrow it with a `result_schema`" rather than a bare subset
  failure. *(open)*
- Harden `derefSubset` to error on an unresolved `super` regardless — a latent
  bug independent of this feature, worth fixing while here.
- Confirm the child-output conform path (`collect.go`) enforces the parent's
  `result_schema` in every child-spawning action type (`child` / `child_map` /
  `child_list`), so narrowing is sound uniformly.

---

## Appendix — Not planned: schema-valued generics

Preserved for the record. This was the more ambitious direction: let a parent pass
a **schema as a value** so a process is generic over its result type
(`fetch<T>(config: { …, result_schema: Schema<T> }): T`), with the parent's result
type *derived* from the schema it passed rather than re-declared.

It was dropped because it buys little over `unknown` + narrow-at-the-boundary
while carrying a large, permanent cost. The pieces it would have required, none of
which the engine has today:

- A first-class **`Schema` builtin type** (a meta-schema over the engine's keyword
  allowlist) plus an **untrusted-schema boundary** (parse a `map[string]any` input
  through the strict path, normalize it *in isolation*, validate it against the
  meta-schema).
- **Call-site specialization**: `Generate(child)` is caller-independent and the
  solver memoizes on def-name only (`internal/schema/solver.go:369-372`), so
  deriving a result type from a caller-supplied schema needs a genuinely new keying
  axis (per-instantiation namespacing by the bound schema's identity).

The recursion machinery itself would *not* have needed changing — a self-contained,
closed, productive injected schema forms its own SCC and rides the existing solver
(termination is structural: finite member set + `maxSolvePasses` + the 64KB
widening cap, with productivity enforced in `CheckDoc`). But the specialization
axis and the untrusted-schema surface are a permanent tax on the engine's hairiest
subsystem, justified by no concrete, recurring use case. If such a use case appears
— genuinely reusable processes callers invoke with differing schemas, where writing
the schema once instead of at each boundary matters — this is where to resume.
