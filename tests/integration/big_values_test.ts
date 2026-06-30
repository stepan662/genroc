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

// A large input flows through the engine to a large output, both transparently
// externalized to the object store and transparently rehydrated by the detail API.
test("big values round-trip through the object store", async () => {
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
  // Context comes back fully hydrated — the big input and the big computed output are
  // their original values, not references.
  expect((data!.context as any).input.blob).toBe(BLOB);
  expect((data!.context as any).output.echo).toBe(BLOB);
});

// A large log payload is externalized: the log row carries a short preview plus a
// data_ref, and the full value is fetchable from the log-object endpoint.
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
  const completed = (data!.items ?? []).find((l) => l.event === "inst_completed");
  expect(completed).toBeDefined();
  // The full output is big, so it is not stored inline: a reference is present and the
  // inline data is only a short preview.
  expect(completed!.data_ref).toBeDefined();
  expect(completed!.data!.length).toBeLessThan(BLOB.length);

  const ref = completed!.data_ref!.ref;
  const { data: obj, error: objErr } = await client.GET(
    "/instances/{id}/objects/{ref}",
    { params: { path: { id, ref } } },
  );
  expect(objErr).toBeUndefined();
  // The fetched payload is the full, untruncated output value.
  expect((obj as any).data).toBe(JSON.stringify({ echo: BLOB }));
});
