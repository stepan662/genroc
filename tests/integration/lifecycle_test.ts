import { assertEquals } from "jsr:@std/assert";
import {
  client,
  startMockService,
  waitForInstance,
} from "../helpers/client.ts";

Deno.test(
  "lifecycle — task step completes when service returns ok",
  async () => {
    const mock = startMockService(19992, {
      status: "ok",
      output: { done: true },
    });

    const name = `lifecycle_ok_${crypto.randomUUID()}`;
    await client.PUT("/definitions", {
      body: {
        name,
        version: 1,
        steps: [
          {
            type: "task" as const,
            id: "call",
            transport: "http" as const,
            endpoint: "http://localhost:19992/action",
            timeout_ms: 2000,
            retries: 0,
          },
        ],
      },
    });

    const { data: startData } = await client.POST("/instances", {
      body: { process: name, input: { x: 1 } },
    });
    const id = startData!.id;

    const status = await waitForInstance(id);
    assertEquals(status, "completed");

    // Verify output was merged into context.
    const { data } = await client.GET("/instances/{id}", {
      params: { path: { id } },
    });
    const ctx = data?.context;
    assertEquals(ctx?.done, true);

    await mock.shutdown();
  },
);

Deno.test(
  "lifecycle — task step fails and retries then marks failed",
  async () => {
    const mock = startMockService(19993, { status: "error", error: "boom" });

    const name = `lifecycle_fail_${crypto.randomUUID()}`;
    await client.PUT("/definitions", {
      body: {
        name,
        version: 1,
        steps: [
          {
            type: "task" as const,
            id: "call",
            transport: "http" as const,
            endpoint: "http://localhost:19993/action",
            timeout_ms: 500,
            retries: 1,
          },
        ],
      },
    });

    const { data: startData } = await client.POST("/instances", {
      body: { process: name },
    });
    const id = startData!.id;

    const status = await waitForInstance(id, 10_000);
    assertEquals(status, "failed");

    await mock.shutdown();
  },
);

Deno.test("lifecycle — conditional routes to correct branch", async () => {
  const thenMock = startMockService(19994, {
    status: "ok",
    output: { branch: "then" },
  });
  const elseMock = startMockService(19995, {
    status: "ok",
    output: { branch: "else" },
  });

  const name = `lifecycle_cond_${crypto.randomUUID()}`;
  await client.PUT("/definitions", {
    body: {
      name,
      version: 1,
      steps: [
        {
          type: "conditional" as const,
          id: "check",
          condition: "context.input.go_then == true",
          then: [
            {
              type: "task" as const,
              id: "then_step",
              transport: "http" as const,
              endpoint: "http://localhost:19994/action",
              timeout_ms: 1000,
              retries: 0,
            },
          ],
          else: [
            {
              type: "task" as const,
              id: "else_step",
              transport: "http" as const,
              endpoint: "http://localhost:19995/action",
              timeout_ms: 1000,
              retries: 0,
            },
          ],
        },
      ],
    },
  });

  // Test then branch.
  const { data: d1 } = await client.POST("/instances", {
    body: { process: name, input: { go_then: true } },
  });
  const id1 = d1!.id;
  await waitForInstance(id1);
  const { data: r1 } = await client.GET("/instances/{id}", {
    params: { path: { id: id1 } },
  });
  assertEquals(r1?.context?.branch, "then");

  // Test else branch.
  const { data: d2 } = await client.POST("/instances", {
    body: { process: name, input: { go_then: false } },
  });
  const id2 = d2!.id;
  await waitForInstance(id2);
  const { data: r2 } = await client.GET("/instances/{id}", {
    params: { path: { id: id2 } },
  });
  assertEquals(r2?.context?.branch, "else");

  await thenMock.shutdown();
  await elseMock.shutdown();
});
