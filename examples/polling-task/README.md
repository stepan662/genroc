# Polling task example

A parent process that spawns a **child process** whose whole job is to kick off a
long-running task on a remote server and then **poll its status until it's done** — or
until a caller cancels it, or an attempt budget runs out. On success the child returns the
job's answer; the two failure modes are **raised** as errors the parent catches.

```
polling-example (parent)
  └─ run: child     ──spawn──▶ poll-until-done (child)
       on_error:                 kickstart  POST {url}/jobs    ─▶ { job_id }
         cancelled ─▶ report      check      POST {url}/status  ─▶ { status, result }
         poll_timeout ─▶ report     ├─ status == "done"     ─▶ finish ─▶ output { answer }
                                    ├─ attempts exhausted   ─▶ give_up  POST {url}/cancel ─▶ raise poll_timeout
                                    └─ else                 ─▶ wait
                                  wait       external (cancel checkpoint)
                                    ├─ cancel signal ─▶ cancel   POST {url}/cancel ─▶ raise cancelled
                                    └─ no signal     ─▶ backoff
                                  backoff    delay ({{ poll_interval_ms }}) ─▶ back to check
```

This is the child → parent error-handling contract (see
[`docs/child-error-handling.md`](../../docs/child-error-handling.md)): success is an
**output**, but a cancellation or timeout is an anticipated error the child **raises** by a
named code, and the parent's `on_error` on the child task **catches** it — here routing both
to a `report` task that reads the raised error from `$error`. A result the caller inspects
would stay in output; control-flow conditions the caller reacts to are raises.

## Files

- [`poller.genroc.yaml`](./poller.genroc.yaml) — the child. Starts the remote job, then
  loops `check → wait → backoff → check` until it's `done`, cancelled, or out of attempts.
- [`parent.genroc.yaml`](./parent.genroc.yaml) — spawns the poller as a child, threads the
  connection details and the (optional) knobs down, collects its answer on success, and
  catches `cancelled` / `poll_timeout` via `on_error` on the child task.

## The polling pattern

genroc has no `while`/`until` keyword. A poll loop is expressed structurally: the `check`
task's `switch` routes to a `wait`/`backoff` pair, which routes back to `$check`, until the
status becomes `done`. Each request is a `fetch` action (an HTTP call like `fetch(url,
{method, headers, body})`, where every field is an expression) that persists and reclaims, so
the loop is crash-safe and holds no worker while it's parked.

## Configuring the poll interval and timeout (parent → child)

Both are **optional input parameters with schema defaults** — declare `default:` on the
`input_schema` property and validation fills it in when the caller omits it, so
`poll-until-done` reads like a function with default arguments. (A defaulted optional is also
inferred as non-nullable, so it's usable directly in expressions — `input.max_attempts` needs
no `?? ` guard.) The parent declares the same defaults and threads the values down to the child:

- **`poll_interval_ms`** (default 500) — the back-off between polls. It lives on the `backoff`
  **`delay`** task, because a delay's `ms` is a templated expression. (A task's `timeout_ms` is
  a static int and can't be templated, which is why the interval isn't the `external`'s timeout.)
- **`max_attempts`** (default 20) — the overall timeout, expressed as a **maximum number of
  status checks**. genroc expressions have no wall clock, so a poll budget is the honest primitive;
  the wall-time budget is roughly `max_attempts × poll_interval_ms`. `check` counts its own
  runs via `self.previous`, and once the budget is spent routes to `give_up`, which cleans up
  the remote job and **raises `poll_timeout`**.

```sh
genctl run polling-example \
  --input '{ "url": "http://localhost:9000",
             "headers": { "Authorization": "Bearer s3cr3t" },
             "poll_interval_ms": 2000, "max_attempts": 30 }'
```

## Cancelling from the wait phase

The `wait` task is a short-lived `external` action, hit once per loop as a **cancel
checkpoint**: it parks until either its `timeout_ms` elapses (→ back off and poll again,
routed by `on_error` on `external.timeout`) or a signal is delivered to it. That signal is
the cancel hook — a caller sends:

```sh
genctl signal <child-instance-id> --task wait --result '{ "cancel": true }'
# or: POST /instances/<child-instance-id>/signal  { "task_id": "wait", "result": { "cancel": true } }
```

The `wait` task's `switch` routes a `cancel: true` result to the `cancel` task, which hits
`POST {url}/cancel` to stop the remote job and then **raises `cancelled`** for the parent to
catch. Signals **buffer**, so a
cancel sent mid-poll is honoured on the next loop — within roughly one poll interval rather
than instantly, which is the trade-off for a runtime-configurable interval (the wait can't be
both a cancellable `external` *and* a templated-duration `delay`). The signal targets the
**child** instance — find it under the parent's `context._children.run` (a single `child`
task records the bare child id there, not a keyed map).

## Configuring headers (parent → child)

The whole request is caller-driven, so the **caller supplies the entire headers map** and
the child splats it onto every `fetch` with `headers: "{{ input.headers }}"`. The headers
input is typed as an **open string map** — `{ type: object, additionalProperties: { type:
string } }` — so arbitrary keys (auth, trace ids, …) flow from the parent down without the
child declaring each one. This is `additionalProperties` in action: without it, undeclared
header keys would be stripped by normalization; with it they survive as typed string values.
A `fetch` action's `headers` is a shape, so it accepts either this whole-map expression or a
literal map of templated values.

genroc also **auto-stamps** `X-Genroc-Instance-Id` and `X-Genroc-Task-Id` on every request
(set authoritatively, so a caller header can't spoof them), so the receiving service can
correlate a call back to the instance/task that made it — the run context the raw body no
longer carries.

## Running it

The service base URL and a headers map are passed as input, so point it at any server exposing
`POST /jobs` (returns `{ "job_id": ... }`), `POST /status` (returns
`{ "status": "pending" | "done", "result": ... }`), and `POST /cancel`:

```sh
genctl apply -f poller.genroc.yaml -f parent.genroc.yaml
genctl run polling-example --input '{
  "url": "http://localhost:9000",
  "headers": { "Authorization": "Bearer s3cr3t" }
}'
```

## Automated test

[`tests/integration/examples_polling_test.ts`](../../tests/integration/examples_polling_test.ts)
loads these YAML files verbatim, applies them against a throwaway mock job service, and
asserts all three outcomes — polling through to `done` (answer returned), cancelling via a
signal (`cancelled` raised and caught), and running out of `max_attempts` (`poll_timeout`
raised and caught) — so this example is also an executable test. Run it with `make test-int`.
