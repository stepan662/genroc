# Polling task example

A parent process that spawns a **child process** whose whole job is to kick off a
long-running task on a remote server and then **poll its status until it's done** — or
until a caller cancels it, or an attempt budget runs out — finally returning the outcome
up to the parent.

```
polling-example (parent)
  └─ run: child_map ──spawn──▶ poll-until-done (child)
                                 kickstart  POST {jobs_url}/jobs    ─▶ { job_id }
                                 check      POST {jobs_url}/status  ─▶ { status, result }
                                   ├─ status == "done"     ─▶ finish ─▶ end
                                   ├─ attempts exhausted   ─▶ give_up  POST {jobs_url}/cancel ─▶ end
                                   └─ else                 ─▶ wait
                                 wait       external (cancel checkpoint)
                                   ├─ cancel signal ─▶ cancel   POST {jobs_url}/cancel ─▶ end
                                   └─ no signal     ─▶ backoff
                                 backoff    delay ({{ poll_interval_ms }}) ─▶ back to check
```

The child always returns `{ cancelled, timed_out, answer }`, so the parent gets a uniform
result whichever way the poll ends.

## Files

- [`poller.genroc.yaml`](./poller.genroc.yaml) — the child. Starts the remote job, then
  loops `check → wait → backoff → check` until it's `done`, cancelled, or out of attempts.
- [`parent.genroc.yaml`](./parent.genroc.yaml) — spawns the poller as a child, threads the
  connection details and the (optional) knobs down, and surfaces its result.

## The polling pattern

genroc has no `while`/`until` keyword. A poll loop is expressed structurally: the `check`
task's `switch` routes to a `wait`/`backoff` pair, which routes back to `$check`, until the
status becomes `done`. Each iteration is a real REST call that persists and reclaims, so the
loop is crash-safe and holds no worker while it's parked.

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
  the remote job and reports `timed_out`.

```sh
genctl run polling-example \
  --input '{ "jobs_url": "http://localhost:9000", "auth_token": "s3cr3t",
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
`POST {jobs_url}/cancel` to stop the remote job and then finishes. Signals **buffer**, so a
cancel sent mid-poll is honoured on the next loop — within roughly one poll interval rather
than instantly, which is the trade-off for a runtime-configurable interval (the wait can't be
both a cancellable `external` *and* a templated-duration `delay`). The signal targets the
**child** instance — find it under the parent's `context._children.run.poll`.

## Configuring headers (parent → child)

A `rest` action takes a `headers` map whose **values are expressions**, so the child sets
`Authorization: "Bearer {{ input.auth_token }}"` on its requests. The parent owns that
token and passes it down through the child's `input` when it spawns it — that's how a
parent configures the headers its child sends: thread the value through child input, then
reference it in the child's `headers` map. (The header *keys* are declared in the child;
only the *values* flow in.)

## Running it

The service base URL and auth token are passed as input, so point it at any server exposing
`POST /jobs` (returns `{ "job_id": ... }`), `POST /status` (returns
`{ "status": "pending" | "done", "result": ... }`), and `POST /cancel`:

```sh
genctl apply -f poller.genroc.yaml -f parent.genroc.yaml
genctl run polling-example --input '{ "jobs_url": "http://localhost:9000", "auth_token": "s3cr3t" }'
```

## Automated test

[`tests/integration/examples_polling_test.ts`](../../tests/integration/examples_polling_test.ts)
loads these YAML files verbatim, applies them against a throwaway mock job service, and
asserts all three outcomes — polling through to `done`, cancelling via a signal, and running
out of `max_attempts` — so this example is also an executable test. Run it with `make test-int`.
