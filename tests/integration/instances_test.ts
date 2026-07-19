import { expect, test } from "vitest";
import { client } from "../helpers/client.ts";

const processName = `test_proc_${crypto.randomUUID()}`;

async function ensureDefinition() {
  await client.PUT("/definitions", {
    body: {
      name: processName,

      input_schema: {
        type: "object",
        properties: { order_id: { type: "number" } },
        required: ["order_id"],
      },
      tasks: [
        {
          id: "s1",
          action: { type: "fetch" as const, url: "http://localhost:19991/action" },
          timeout_ms: 500,
          switch: [{ goto: "end" }],
        },
      ],
    },
  });
}

test("POST /instances — starts a new instance", async () => {
  await ensureDefinition();

  const { data, error } = await client.POST("/instances", {
    body: { process: processName, input: { order_id: 1 } },
  });

  expect(error).toBeUndefined();
  expect(data!.id).toBeDefined();
  expect(data!.status).toBe("running");
});

test("GET /instances/{id} — returns instance status", async () => {
  await ensureDefinition();

  const { data: startData, error: startError } = await client.POST(
    "/instances",
    {
      body: { process: processName, input: { order_id: 1 } },
    },
  );

  expect(startError).toBeUndefined();
  const id = startData!.id;

  const { data, error } = await client.GET("/instances/{id}", {
    params: { path: { id } },
  });
  expect(error).toBeUndefined();
  expect(data!.id).toBe(id);
});

test("GET /instances/{id} — returns error for unknown ID", async () => {
  const { data, error } = await client.GET("/instances/{id}", {
    params: { path: { id: "00000000-0000-0000-0000-000000000000" } },
  });
  expect(error).toBeDefined();
  expect(data?.context).toBeUndefined();
});

test("GET /instances — lists instances", async () => {
  const { data, error } = await client.GET("/instances");
  expect(error).toBeUndefined();
  expect(Array.isArray(data!.items)).toBe(true);
});

test("GET /instances — list items omit context but carry timestamps", async () => {
  await ensureDefinition();
  await client.POST("/instances", { body: { process: processName, input: { order_id: 1 } } });

  const { data, error } = await client.GET("/instances", {
    params: { query: { limit: 5 } },
  });
  expect(error).toBeUndefined();
  const items = data!.items ?? [];
  expect(items.length).toBeGreaterThan(0);

  const item = items[0];
  // The (potentially large) context is never returned by the list.
  expect("context" in item).toBe(false);
  // The scalar summary fields are present.
  expect(item.id).toBeDefined();
  expect(item.status).toBeDefined();
  expect(item.created_at).toBeDefined();
  expect(item.updated_at).toBeDefined();

  // Default sort is created (an immutable key, stable for cursor walks).
  expect(data!.page.sort).toBe("created");
  expect(data!.page.order).toBe("desc");
});

test("GET /instances?sort=updated — updated is an opt-in sort", async () => {
  const { data, error } = await client.GET("/instances", {
    params: { query: { sort: "updated", limit: 5 } },
  });
  expect(error).toBeUndefined();
  expect(data!.page.sort).toBe("updated");
});

test("GET /instances/{id} — detail includes the full context", async () => {
  await ensureDefinition();
  const { data: started } = await client.POST("/instances", {
    body: { process: processName, input: { order_id: 99 } },
  });

  const { data, error } = await client.GET("/instances/{id}", {
    params: { path: { id: started!.id } },
  });
  expect(error).toBeUndefined();
  expect(data!.context).toBeDefined();
  expect((data!.context as Record<string, unknown>).input).toEqual({ order_id: 99 });
});

test("POST /instances — fails when input is invalid", async () => {
  await ensureDefinition();

  const { data, error } = await client.POST("/instances", {
    body: { process: processName, input: { order_id: "hi" } },
  });

  expect(error).toBeDefined();
  expect(data).toBeUndefined();
});

test("POST /instances — what happens when referencing types?", async () => {
  await client.PUT("/definitions", {
    body: {
      name: processName,

      input_schema: {
        $ref: "#/$defs/order",
        $defs: {
          order: {
            type: "object",
            properties: {
              order_id: { type: "number" },
            },
            required: ["order_id"],
          },
        },
      },
      tasks: [
        {
          id: "s1",
          action: { type: "fetch" as const, url: "http://localhost:19991/action" },
          timeout_ms: 500,
          switch: [{ goto: "end" }],
        },
      ],
    },
  });

  const { data, error } = await client.POST("/instances", {
    body: { process: processName, input: { order_id: 10 } },
  });

  expect(data).toBeDefined();
  expect(undefined).toBeUndefined();
});
