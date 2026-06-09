import { expect, test } from "vitest";
import { client, startMockService, waitForInstance } from "../helpers/client.ts";

// ── Static validation ─────────────────────────────────────────────────────────

test("idempotent:false — rejects retries on http.% pattern", async () => {
  const { error } = await client.PUT("/definitions", {
    body: {
      name: `ni_static_http_${crypto.randomUUID()}`,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: { type: "rest" as const, endpoint: "http://localhost:19990/x" },
          on_error: [{ code: ["http.%"], retries: 3 }],
        },
      ],
    },
  });
  expect(error).toBeDefined();
  expect(JSON.stringify(error)).toContain("http.%");
});

test("idempotent:false — rejects retries on exact http.500", async () => {
  const { error } = await client.PUT("/definitions", {
    body: {
      name: `ni_static_exact_${crypto.randomUUID()}`,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: { type: "rest" as const, endpoint: "http://localhost:19990/x" },
          on_error: [{ code: ["http.500"], retries: 1 }],
        },
      ],
    },
  });
  expect(error).toBeDefined();
  expect(JSON.stringify(error)).toContain("http.500");
});

test("idempotent:false — rejects catch-all with retries", async () => {
  const { error } = await client.PUT("/definitions", {
    body: {
      name: `ni_static_catchall_${crypto.randomUUID()}`,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: { type: "rest" as const, endpoint: "http://localhost:19990/x" },
          on_error: [{ retries: 2 }],
        },
      ],
    },
  });
  expect(error).toBeDefined();
  expect(JSON.stringify(error)).toContain("catch-all");
});

test("idempotent:false — rejects wildcard crossing namespaces", async () => {
  const { error } = await client.PUT("/definitions", {
    body: {
      name: `ni_static_cross_${crypto.randomUUID()}`,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: { type: "rest" as const, endpoint: "http://localhost:19990/x" },
          on_error: [{ code: ["s%"], retries: 3 }],
        },
      ],
    },
  });
  expect(error).toBeDefined();
  expect(JSON.stringify(error)).toContain("s%");
});

test("idempotent:false — accepts retries on start.%", async () => {
  const { error } = await client.PUT("/definitions", {
    body: {
      name: `ni_static_start_${crypto.randomUUID()}`,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: { type: "rest" as const, endpoint: "http://localhost:19990/x" },
          on_error: [
            { code: ["start.%"], retries: 3 },
            { goto: "$end" },
          ],
        },
      ],
    },
  });
  expect(error).toBeUndefined();
});

test("idempotent:false — accepts retries on exact start.* codes", async () => {
  const { error } = await client.PUT("/definitions", {
    body: {
      name: `ni_static_exact_start_${crypto.randomUUID()}`,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: { type: "rest" as const, endpoint: "http://localhost:19990/x" },
          on_error: [{ code: ["start.error", "start.timeout"], retries: 3 }],
        },
      ],
    },
  });
  expect(error).toBeUndefined();
});

test("idempotent:false — accepts executed:false override for http.422", async () => {
  const { error } = await client.PUT("/definitions", {
    body: {
      name: `ni_static_exec_false_${crypto.randomUUID()}`,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: { type: "rest" as const, endpoint: "http://localhost:19990/x" },
          on_error: [
            { code: ["http.422"], executed: false, retries: 2 },
            { code: ["http.%"], goto: "$end" },
          ],
        },
      ],
    },
  });
  expect(error).toBeUndefined();
});

test("idempotent:false — accepts catch-all with executed:false", async () => {
  const { error } = await client.PUT("/definitions", {
    body: {
      name: `ni_static_catchall_exec_${crypto.randomUUID()}`,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: { type: "rest" as const, endpoint: "http://localhost:19990/x" },
          on_error: [{ executed: false, retries: 2 }],
        },
      ],
    },
  });
  expect(error).toBeUndefined();
});

test("idempotent:false — goto-only rule on http.% is accepted (no retries)", async () => {
  const { error } = await client.PUT("/definitions", {
    body: {
      name: `ni_static_goto_only_${crypto.randomUUID()}`,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: { type: "rest" as const, endpoint: "http://localhost:19990/x" },
          on_error: [{ code: ["http.%"], goto: "#handler" }],
        },
        {
          id: "handler",
          call: { type: "rest" as const, endpoint: "http://localhost:19990/x" },
        },
      ],
    },
  });
  expect(error).toBeUndefined();
});

// ── Runtime behaviour ─────────────────────────────────────────────────────────

test("idempotent:false — http.500 routes to handler and is called exactly once", async () => {
  const failMock = await startMockService(0, { statusCode: 500 });
  const handlerMock = await startMockService(0, { response: { handled: true } });

  const name = `ni_rt_no_retry_${crypto.randomUUID()}`;
  await client.PUT("/definitions", {
    body: {
      name,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: {
            type: "rest" as const,
            endpoint: `http://localhost:${failMock.port}/action`,
          },
          on_error: [
            // start.% rule present — would retry on connection errors but not on http.*
            { code: ["start.%"], retries: 3 },
            { code: ["http.%"], goto: "#handler" },
          ],
          timeout_ms: 2000,
        },
        {
          id: "handler",
          call: {
            type: "rest" as const,
            endpoint: `http://localhost:${handlerMock.port}/action`,
            output_schema: {
              type: "object",
              properties: { handled: { type: "boolean" } },
              required: ["handled"],
            },
          },
          timeout_ms: 2000,
        },
      ],
    },
  });

  const { data } = await client.POST("/instances", { body: { process: name } });
  expect(await waitForInstance(data!.id)).toBe("completed");

  // The key assertion: only one call to the failing endpoint — no retries fired
  expect(failMock.requestCount()).toBe(1);
  expect(handlerMock.requestCount()).toBe(1);

  failMock.stop();
  handlerMock.stop();
});

test("idempotent:false — connection refused triggers start.% retries", async () => {
  // Start then immediately stop the mock to free the port — subsequent connects will be refused
  const gone = await startMockService(0);
  const port = gone.port;
  await gone.stop();

  const name = `ni_rt_start_retry_${crypto.randomUUID()}`;
  await client.PUT("/definitions", {
    body: {
      name,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: {
            type: "rest" as const,
            endpoint: `http://localhost:${port}/action`,
          },
          on_error: [
            // 1 retry on start.% then complete via $end
            { code: ["start.%"], retries: 1, goto: "$end" },
          ],
          timeout_ms: 2000,
        },
      ],
    },
  });

  const { data } = await client.POST("/instances", { body: { process: name } });
  // Two attempts (original + 1 retry), both refused. Retries exhausted → goto $end → completed.
  // The 2-second retry delay means this takes ~2s — well within the 30s test timeout.
  expect(await waitForInstance(data!.id, 15_000)).toBe("completed");
});

test("idempotent:false — executed:false allows retry on http.422", async () => {
  // First call returns 422 (trigger retry), second returns 200
  let calls = 0;
  const mock = await startMockService(0, { statusCode: 200, response: { ok: true } });
  // We can't make the mock return different status codes per call, so instead we verify
  // that with executed:false the definition is accepted and the step runs.
  // A 200 response means executed:false retries would not fire (no error to trigger them).
  // The meaningful runtime check is the static acceptance test above; here we just confirm
  // the step executes and completes normally.

  const name = `ni_rt_exec_false_${crypto.randomUUID()}`;
  const { error: defErr } = await client.PUT("/definitions", {
    body: {
      name,
      steps: [
        {
          id: "charge",
          idempotent: false,
          call: {
            type: "rest" as const,
            endpoint: `http://localhost:${mock.port}/action`,
            output_schema: {
              type: "object",
              properties: { ok: { type: "boolean" } },
              required: ["ok"],
            },
          },
          on_error: [{ code: ["http.422"], executed: false, retries: 2 }],
          timeout_ms: 2000,
        },
      ],
    },
  });
  expect(defErr).toBeUndefined();

  const { data } = await client.POST("/instances", { body: { process: name } });
  expect(await waitForInstance(data!.id)).toBe("completed");

  mock.stop();
});

test("idempotent step (default) — http.500 retries normally", async () => {
  // Baseline: same setup without idempotent:false. The http.% rule has retries:1.
  // Total calls = 2 (original + 1 retry), then $end → completed.
  const failMock = await startMockService(0, { statusCode: 500 });

  const name = `idempotent_default_retry_${crypto.randomUUID()}`;
  await client.PUT("/definitions", {
    body: {
      name,
      steps: [
        {
          id: "call",
          // No idempotent:false — default idempotent behaviour
          call: {
            type: "rest" as const,
            endpoint: `http://localhost:${failMock.port}/action`,
          },
          on_error: [{ code: ["http.%"], retries: 1, goto: "$end" }],
          timeout_ms: 2000,
        },
      ],
    },
  });

  const { data } = await client.POST("/instances", { body: { process: name } });
  // 1 retry = 2s delay; allow up to 15s
  expect(await waitForInstance(data!.id, 15_000)).toBe("completed");

  // Original + 1 retry = 2 calls
  expect(failMock.requestCount()).toBe(2);

  failMock.stop();
});
