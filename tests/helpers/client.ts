import createClient from "openapi-fetch";
import { createServer } from "http";
import type { paths } from "../generated/api.ts";
import { BASE_URL } from "./constants.ts";

export const client = createClient<paths>({ baseUrl: BASE_URL });

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

export function startMockService(
  port: number,
  response: Record<string, unknown> = { status: "ok", output: {} },
) {
  const body = JSON.stringify(response);
  const server = createServer((_req, res) => {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(body);
  });
  server.listen(port);
  return { stop: () => server.close() };
}
