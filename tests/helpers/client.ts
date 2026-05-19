import createClient from "npm:openapi-fetch";
import type { paths } from "../generated/api.ts";

const BASE_URL = Deno.env.get("GENT_URL") ?? "http://localhost:8080";

export const client = createClient<paths>({ baseUrl: BASE_URL });

/** Poll until the instance reaches a terminal status or timeout. */
export async function waitForInstance(
  id: string,
  timeoutMs = 5000,
): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const { data, error } = await client.GET("/instances/{id}", {
      params: { path: { id } },
    });
    if (error) throw new Error(`get_instance failed: ${JSON.stringify(error)}`);
    const status = data?.status;
    if (status === "completed" || status === "failed") return status!;
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`instance ${id} did not complete within ${timeoutMs}ms`);
}

/** Start a minimal HTTP mock service on port that returns a fixed response. */
export function startMockService(
  port: number,
  response: Record<string, unknown> = { status: "ok", output: {} },
): Deno.HttpServer<Deno.NetAddr> {
  return Deno.serve(
    { port, onListen: () => {} },
    () =>
      new Response(JSON.stringify(response), {
        headers: { "content-type": "application/json" },
      }),
  );
}
