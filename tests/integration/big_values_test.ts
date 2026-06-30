import { expect, test } from "vitest";
import { client, waitForInstance } from "../helpers/client.ts";

const proc = `big_values_${crypto.randomUUID()}`;

// A value larger than the externalization threshold (8 KiB) so it is stored in the
// object store rather than inline on the instance row.
const BLOB = "B".repeat(20 * 1024);

async function defineProc() {
  await client.PUT("/definitions", {
    body: {
      name: proc,
      input_schema: {
        type: "object",
        properties: { blob: { type: "string" } },
        required: ["blob"],
      },
      // The output reads the (externalized) input, exercising lazy resolution through
      // the engine's output projection.
      output: { echo: "{{ input.blob }}" },
      tasks: [{ id: "work", switch: [{ goto: "end" }] }],
    },
  });
}

// By default a large input/output slot is NOT pulled out of the object store: the
// detail view returns a lightweight {ref, size} reference instead of the value.
test("big values are returned as references by default", async () => {
  await defineProc();
  const { data: started } = await client.POST("/instances", {
    body: { process: proc, input: { blob: BLOB } },
  });
  const id = started!.id;
  await waitForInstance(id);

  const { data, error } = await client.GET("/instances/{id}", {
    params: { path: { id } },
  });
  expect(error).toBeUndefined();
  // The big input and the big computed output come back as references, not values.
  const input = (data!.context as any).input;
  const output = (data!.context as any).output;
  expect(typeof input.ref).toBe("string");
  expect(input.size).toBeGreaterThan(BLOB.length);
  expect(input.blob).toBeUndefined();
  expect(typeof output.ref).toBe("string");
  expect(output.echo).toBeUndefined();
});

// With ?resolve=true the caller opts into fully materialized context: references are
// replaced by their original values.
test("big values are resolved inline with resolve=true", async () => {
  await defineProc();
  const { data: started } = await client.POST("/instances", {
    body: { process: proc, input: { blob: BLOB } },
  });
  const id = started!.id;
  await waitForInstance(id);

  const { data, error } = await client.GET("/instances/{id}", {
    params: { path: { id }, query: { resolve: true } },
  });
  expect(error).toBeUndefined();
  expect((data!.context as any).input.blob).toBe(BLOB);
  expect((data!.context as any).output.echo).toBe(BLOB);
});

// A large log payload is externalized: by default the log row carries only a data_ref
// (no inline data), and the full value is fetchable from the log-object endpoint.
test("large log payloads are externalized and fetchable", async () => {
  await defineProc();
  const { data: started } = await client.POST("/instances", {
    body: { process: proc, input: { blob: BLOB } },
  });
  const id = started!.id;
  await waitForInstance(id);

  const { data, error } = await client.GET("/instances/{id}/logs", {
    params: { path: { id }, query: { limit: 100 } },
  });
  expect(error).toBeUndefined();
  const completed = (data!.items ?? []).find(
    (l) => l.event === "inst_completed",
  );
  expect(completed).toBeDefined();
  // The full output is big, so it is not stored inline: by default the log carries only
  // a bare reference, with no inline data/preview.
  expect(completed!.data_ref).toBeDefined();
  expect(completed!.data).toBeFalsy();

  const ref = completed!.data_ref!.ref;
  const { data: obj, error: objErr } = await client.GET(
    "/instances/{id}/objects/{ref}",
    { params: { path: { id, ref } } },
  );
  expect(objErr).toBeUndefined();
  // The fetched payload is the full, untruncated output value.
  expect((obj as any).data).toBe(JSON.stringify({ echo: BLOB }));
});

// With ?resolve=true the log listing inlines the full payload directly, dropping the
// preview + data_ref indirection.
test("log payloads are inlined with resolve=true", async () => {
  await defineProc();
  const { data: started } = await client.POST("/instances", {
    body: { process: proc, input: { blob: BLOB } },
  });
  const id = started!.id;
  await waitForInstance(id);

  const { data, error } = await client.GET("/instances/{id}/logs", {
    params: { path: { id }, query: { limit: 100, resolve: true } },
  });
  expect(error).toBeUndefined();
  const completed = (data!.items ?? []).find(
    (l) => l.event === "inst_completed",
  );
  expect(completed).toBeDefined();
  // The full payload is inlined; no reference is left behind.
  expect(completed!.data_ref).toBeUndefined();
  expect(completed!.data).toBe(JSON.stringify({ echo: BLOB }));
});

// Within one instance, only the slots that exceed the threshold are externalized: a
// small slot stays an inline value even when a sibling slot is a reference.
test("only oversized slots become references; small ones stay inline", async () => {
  const name = `mixed_slots_${crypto.randomUUID()}`;
  await client.PUT("/definitions", {
    body: {
      name,
      input_schema: {
        type: "object",
        properties: { blob: { type: "string" } },
        required: ["blob"],
      },
      // input is big (externalized); output is a small constant (stays inline).
      output: { ok: "done" },
      tasks: [{ id: "work", switch: [{ goto: "end" }] }],
    },
  });
  const { data: started } = await client.POST("/instances", {
    body: { process: name, input: { blob: BLOB } },
  });
  const id = started!.id;
  await waitForInstance(id);

  const { data, error } = await client.GET("/instances/{id}", {
    params: { path: { id } },
  });
  expect(error).toBeUndefined();
  // Big input → reference; small output → its inline value, not a reference.
  expect(typeof (data!.context as any).input.ref).toBe("string");
  expect((data!.context as any).output).toEqual({ ok: "done" });
});

// SECURITY: a secret nested inside a LARGE (externalized) value must not leak when the
// caller asks to resolve. resolve=true loads the raw stored object — which holds the
// secret in the clear — so redaction has to run over the materialized value, not be
// skipped because the slot arrived as a reference.
test("a secret in an externalized value is still redacted under resolve=true", async () => {
  const name = `secret_big_ctx_${crypto.randomUUID()}`;
  const secret = "S".repeat(20 * 1024); // > threshold → the input slot is externalized
  await client.PUT("/definitions", {
    body: {
      name,
      input_schema: {
        type: "object",
        required: ["token"],
        properties: { token: { type: "string", secret: true } },
      },
      tasks: [{ id: "work", switch: [{ goto: "end" }] }],
    },
  });
  const { data: started } = await client.POST("/instances", {
    body: { process: name, input: { token: secret } },
  });
  const id = started!.id;
  await waitForInstance(id);

  // Default: the value is never loaded — a bare reference, no secret anywhere.
  const { data: lazy } = await client.GET("/instances/{id}", {
    params: { path: { id } },
  });
  expect(typeof (lazy!.context as any).input.ref).toBe("string");
  expect(JSON.stringify(lazy)).not.toContain("SSSSSSSSSS");

  // Resolved: the object is materialized, but the secret field must come back "***".
  const { data: full } = await client.GET("/instances/{id}", {
    params: { path: { id }, query: { resolve: true } },
  });
  expect((full!.context as any).input.token).toBe("***");
  expect(JSON.stringify(full)).not.toContain("SSSSSSSSSS");
});

// A subtree log's externalized payload is owned by the instance that wrote it — a child,
// not the queried root. resolve=true over a recursive listing must fetch each object
// from its own instance, so the CHILD's big payload comes back fully inlined.
test("recursive + resolve inlines a child instance's externalized payload", async () => {
  const child = `recos_child_${crypto.randomUUID()}`;
  const parent = `recos_parent_${crypto.randomUUID()}`;
  await client.PUT("/definitions", {
    body: {
      name: child,
      input_schema: {
        type: "object",
        properties: { blob: { type: "string" } },
        required: ["blob"],
      },
      tasks: [{ id: "leaf", switch: [{ goto: "end" }] }],
    },
  });
  await client.PUT("/definitions", {
    body: {
      name: parent,
      input_schema: {
        type: "object",
        properties: { blob: { type: "string" } },
        required: ["blob"],
      },
      tasks: [
        {
          id: "spawn",
          action: {
            type: "child" as const,
            name: child,
            input: { blob: "{{ input.blob }}" },
          },
          switch: [{ goto: "end" }],
        },
      ],
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
    } as any,
  });

  const { data: started } = await client.POST("/instances", {
    body: { process: parent, input: { blob: BLOB } },
  });
  const id = started!.id;
  expect(await waitForInstance(id, 10_000)).toBe("completed");

  // The child's inst_created log carries the big (externalized) input it received.
  const childCreated = (q: { resolve?: boolean }) =>
    client
      .GET("/instances/{id}/logs", {
        params: { path: { id }, query: { limit: 200, recursive: true, ...q } },
      })
      .then(({ data }) =>
        (data!.items ?? []).find(
          (l) => l.event === "inst_created" && l.instance !== id,
        ),
      );

  // Default: a bare reference on the child's row.
  const lazy = await childCreated({});
  expect(lazy).toBeDefined();
  expect(lazy!.data_ref).toBeDefined();
  expect(lazy!.data).toBeFalsy();

  // Resolved: fetched from the child's own instance and inlined in full.
  const full = await childCreated({ resolve: true });
  expect(full).toBeDefined();
  expect(full!.data_ref).toBeUndefined();
  expect(full!.data).toBe(JSON.stringify({ blob: BLOB }));
});

// A big value passed into a child's input and returned in the child's output flows all
// the way back: the parent collects the child's (externalized) output, re-externalizes
// it into its own context, and exposes it as a reference by default / the full value
// under resolve. Exercises the collect path (child output → parent) with a large value.
test("a big value round-trips through a child's input and output back to the parent", async () => {
  const child = `bv_rt_child_${crypto.randomUUID()}`;
  const parent = `bv_rt_parent_${crypto.randomUUID()}`;
  await client.PUT("/definitions", {
    body: {
      name: child,
      input_schema: {
        type: "object",
        properties: { blob: { type: "string" } },
        required: ["blob"],
      },
      // The child returns the big value it received straight back in its output.
      output: { echo: "{{ input.blob }}" },
      tasks: [{ id: "leaf", switch: [{ goto: "end" }] }],
    },
  });
  await client.PUT("/definitions", {
    body: {
      name: parent,
      input_schema: {
        type: "object",
        properties: { blob: { type: "string" } },
        required: ["blob"],
      },
      tasks: [
        {
          id: "spawn",
          action: {
            type: "child" as const,
            name: child,
            input: { blob: "{{ input.blob }}" },
            result_schema: {
              type: "object",
              properties: { echo: { type: "string" } },
              required: ["echo"],
            },
          },
          // Collect the child's (big) output into this task's output…
          output: "{{ self.result }}",
          switch: [{ goto: "end" }],
        },
      ],
      // …and surface it again as the parent's own output.
      output: { echo: "{{ outputs.spawn.echo }}" },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
    } as any,
  });

  const { data: started } = await client.POST("/instances", {
    body: { process: parent, input: { blob: BLOB } },
  });
  const id = started!.id;
  expect(await waitForInstance(id, 10_000)).toBe("completed");

  // Default: the collected child output and the parent's own output are both references.
  const { data: lazy, error } = await client.GET("/instances/{id}", {
    params: { path: { id } },
  });
  expect(error).toBeUndefined();
  expect(typeof (lazy!.context as any).outputs.spawn.ref).toBe("string");
  expect(typeof (lazy!.context as any).output.ref).toBe("string");

  // Resolved: the big value is intact after the full parent → child → parent round-trip.
  const { data: full } = await client.GET("/instances/{id}", {
    params: { path: { id }, query: { resolve: true } },
  });
  expect((full!.context as any).outputs.spawn.echo).toBe(BLOB);
  expect((full!.context as any).output.echo).toBe(BLOB);
});
