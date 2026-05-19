import { assertEquals, assertExists } from "jsr:@std/assert";
import { client } from "../helpers/client.ts";

const processName = `test_proc_${crypto.randomUUID()}`;

// Register a definition once for all instance tests.
async function ensureDefinition() {
  await client.PUT("/definitions", {
    body: {
      name: processName,
      version: 1,
      steps: [
        {
          type: "task" as const,
          id: "s1",
          transport: "http" as const,
          endpoint: "http://localhost:19991/action",
          timeout_ms: 500,
          retries: 0,
        },
      ],
    },
  });
}

Deno.test("POST /instances — starts a new instance", async () => {
  await ensureDefinition();

  const { data, error } = await client.POST("/instances", {
    body: { process: processName, input: { order_id: 1 } },
  });

  assertEquals(error, undefined);

  const inst = data!;
  assertExists(inst.id);
  assertEquals(inst.status, "running");
});

Deno.test("GET /instances/{id} — returns instance status", async () => {
  await ensureDefinition();

  const { data: startData } = await client.POST("/instances", {
    body: { process: processName },
  });

  const id = startData!.id;

  const { data, error } = await client.GET("/instances/{id}", {
    params: { path: { id } },
  });
  assertEquals(error, undefined);
  assertEquals(data!.id, id);
});

Deno.test("GET /instances/{id} — returns error for unknown ID", async () => {
  const { data } = await client.GET("/instances/{id}", {
    params: { path: { id: "00000000-0000-0000-0000-000000000000" } },
  });
  assertEquals(data?.context, undefined);
});

Deno.test("GET /instances — lists instances", async () => {
  const { data, error } = await client.GET("/instances");
  assertEquals(error, undefined);
  assertEquals(Array.isArray(data), true);
});
