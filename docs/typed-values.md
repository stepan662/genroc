# Typed values: dynamic structure in YAML or expression, checked against a schema

Status: **DRAFT / proposal, 2026-07-21. Not implemented.** Captures a concept
worked out in discussion; open details are marked *(open)*. Engine facts are cited
to `file:line`. Related: [the `unknown` type](unknown-type.md) — orthogonal;
that one is about opaque results, this one about typed structured values.

## The idea in one line

At **any node** of a value, the author can either write the structure **literally
in YAML** (with expressions at the leaves) **or** hand over a **single expression**
that resolves to the whole structure — and either way the result is checked, by
its *inferred type*, against a required schema.

This lets you slide the static/dynamic boundary anywhere: keep the parts you know
as YAML, express the parts you don't. It generalizes what `input` already does to
every place that takes structured, expression-bearing input — most importantly an
action's payload.

## What exists today

`Shape` (`internal/model/wire.go:143-151`) is already half of this:

```
Shape = string | { k: Shape, … }
```

- a **string** leaf is an expression (or literal text if it has no `{{ }}`);
- an **object** is a literal whose values are recursively Shapes.

Child `input` is validated exactly the way we want to generalize: infer the
Shape's type, then `IsSubset` against the child's `input_schema`
(`internal/validation/validate_children.go:170-192`). At runtime the Shape is
evaluated against context, and `conform` fills `default:`s and validates
(`internal/schema/validate.go:105`).

What's missing: arrays and scalar literals in the grammar, and applying the same
"infer → subset" check to action payloads (which today are validated by a
hand-written Go switch, not a schema — `validateActionRequiredFields`,
`internal/model/validate.go:117-151`).

## The grammar (generalized)

```
Value = string                 // expression (or literal text if no {{ }})
      | number | bool | null   // literal scalar   (NEW)
      | [ Value, … ]           // literal array    (NEW)
      | { k: Value, … }        // literal object   (exists)
```

- **Strings** need no change — a no-brace string is literal text (type `string`);
  a `{{ }}` string is an expression whose inferred type is used.
- **Scalar literals** (`retries: 3`, `enabled: true`) are new — today you'd have
  to write `"{{ 3 }}"`. genctl already preserves numeric literals through
  YAML→JSON (`cmd/genctl/input.go:80-141`), so the plumbing exists.
- **Arrays** are new. The engine's `items` is a *single* schema (no tuples), so a
  literal `[a, b, c]` infers to `array<join of element types>`, checked
  element-wise by `IsSubset`. `inferArray` already exists for array literals
  inside expressions.

The literal-vs-expression decision is **structural, at authoring time**: a string
is an expression, a map/array is a literal built recursively (`inferShape`,
`internal/validation/infer.go:183-224`). A whole subtree can always be replaced by
a single expression that yields it — that is the "either YAML or expression" duality.

## Two operations

- **`Fits(value, target)` — author time.** `inferShape(value)` then
  `IsSubset(target)`. Purely type-level; it does **not** evaluate expressions or
  fill defaults. This is the check that says "this structure/expression can
  produce something of the required shape".
- **`eval + conform` — runtime.** Evaluate the value against context, then
  `conform` it against the schema — validating concrete values and filling
  `default:`s (`internal/schema/validate.go:105`, `propDefault` at `:274-287`).

This is the same two-layer split the engine already uses (`Infer`/`IsSubset`
static, `conform`/`Validate` runtime). Author-time proves shape; runtime enforces
values and applies defaults.

Strictness is the goal — no `any`. `IsSubset` already refuses an untyped/`any`
source into a typed target (`validate_children.go:206-207`), which is the behavior
we want: if you hand a slot an expression whose type can't be proven to fit, it's
an error, not a silent pass.

## Where it applies

- **Action payloads.** Each action type declares a schema for its payload; the
  authored payload is a `Value`, checked with `Fits`. The hand-written
  `validateActionRequiredFields` switch collapses into a per-type schema + one
  `Fits` call, and per-type defaults (e.g. fetch `method: POST`) move out of Go
  and into that schema.
- **`input`** (child / external) — already works this way against `input_schema`;
  this just widens the grammar (arrays, scalars) uniformly.
- **`output`** — the *grammar* applies (it's a `Value`), but there's **no schema
  to check against**; it's a free projection into `outputs.<id>`. So `Fits` is
  skipped there — the schema is optional, the grammar is universal.

**Not** for statically-defined fields: `id`, and the child family's
`name`/`version`, stay plain typed fields with no expressions — downstream
analysis needs their concrete values, so those opt out of the `Value` grammar.

## Editor support

Because the check is a plain schema, the same schema can drive editor
autocomplete/validation. Generate a **standard JSON Schema** for `.genroc.yaml`
and wire it via yaml-language-server (a `# yaml-language-server: $schema=…`
modeline, or a `yaml.schemas` glob). The serializer already exists — the OpenAPI
`ModelAction` schema is emitted the same way (`JSONSchemaBytes`,
`internal/model/definition.go:83-176`).

Each expression-capable slot becomes `oneOf: [ {type:string}, <structureSchema> ]`,
so the editor offers **property completion for the structured branch** and accepts
an expression string otherwise. This tooling schema is *standard* JSON Schema, so
the custom engine's lack of `additionalProperties:false` / `minProperties` does
not matter here.

Scope, honestly:

- **Easy, ships with the schema:** action-payload hints. Payload schemas are fixed
  per action type, so the editor can complete `url`/`method`/`accepted_status`/…
  from a static generated schema.
- **Needs a language server (deferred):** child `input` hints for *that specific
  child's* properties. The target schema there is chosen by a sibling `name:`
  pointing at another process — a static JSON Schema can't resolve it. That's a
  separate LSP project, not a byproduct of this work.

Minor wrinkle: `oneOf` with a string branch at every leaf can make
yaml-language-server's completion a little noisy (it can't always narrow which
branch you're in). Livable.

## Ledger

**The build:**
- Widen the `Value` grammar: arrays + scalar literals (`checkShape`,
  `internal/model/wire.go:215-229`; `inferShape`, `infer.go:183-224`).
- Ensure `IsSubset` has an array arm (element-wise via `items`).
- Give each action type a payload schema; replace `validateActionRequiredFields`
  with `Fits` + move per-type defaults into the schema.
- Emit a standard JSON Schema for editor tooling (`oneOf:[string, structure]` per
  slot).

**Reused as-is:**
- `inferShape` / `IsSubset` (the child-input check, generalized).
- `conform` + `default:` filling at runtime.
- The JSON-Schema emitter used for OpenAPI.

## Open questions (settle later)

- **Author-time strictness on unknown properties.** Runtime `conform` silently
  *strips* undeclared keys; author-time `Fits` should probably **reject** them so
  the editor flags typos. Decide the fork. *(open)*
- **Array typing.** Homogeneous `array<join of elements>` (matches the engine's
  single `items`) vs. any desire for tuple-like positional types (not supported by
  the engine today — would be a bigger change). Proposal: homogeneous only. *(open)*
- **Scalar-literal edge cases.** A bare string is literal text; confirm no
  ambiguity is introduced by also allowing bare numbers/bools/null. *(open)*
- **Composition with the action tagging change.** This concept wants the payload to
  be one addressable `Value`; that pairs naturally with adjacent/external tagging
  (`action: { type, with: <Value> }` or `action: { fetch: <Value> }`). Sequence
  the two together. *(open)*
