import { createServer } from "http";
import type { AddressInfo } from "net";
import { readFileSync } from "node:fs";
import { load as loadYaml } from "js-yaml";
import { expect, test } from "vitest";
import { client, listAllInstances, waitForInstance } from "../helpers/client.ts";

// The definitions under test are the real example files in examples/polling-task/,
// loaded and applied verbatim — so this test doubles as an executable check that the
// shipped example works end to end. (Vitest's bundler can't `import` a .yaml file, so
// we read + parse the source instead.)
const EXAMPLES = new URL("../../examples/polling-task/", import.meta.url);
function loadDef(file: string): any {
  return loadYaml(readFileSync(new URL(file, EXAMPLES), "utf8"));
}
const poller = loadDef("poller.genroc.yaml");
const parent = loadDef("parent.genroc.yaml");

// startJobService stands in for the remote server the poller talks to.
//   POST /jobs   -> { job_id }                  starts a job
//   POST /status -> { status: pending|done, … } "pending" for the first `pendingPolls`
//                                               checks of a job, then "done" with `result`
//   POST /cancel -> { cancelled: true }         stops the job (cancel or timeout cleanup)
// Every request must carry `expectedAuth` or it's rejected 401 — so a completed run
// proves the auth header the parent set reached the service on each call. Route hit
// counts and the distinct Authorization values seen are recorded for assertions.
async function startJobService(
  pendingPolls: number,
  result: Record<string, unknown>,
  expectedAuth: string,
) {
  let startCount = 0;
  let statusRequests = 0;
  let cancelCount = 0;
  const pollsByJob = new Map<string, number>();
  const authSeen = new Set<string>();

  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c) => chunks.push(c as Buffer));
    req.on("end", () => {
      const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {};
      const send = (code: number, obj: unknown) => {
        res.writeHead(code, { "Content-Type": "application/json" });
        res.end(JSON.stringify(obj));
      };

      const auth = req.headers["authorization"];
      if (typeof auth === "string") authSeen.add(auth);
      if (auth !== expectedAuth) return send(401, { error: "unauthorized" });

      if (req.url === "/jobs") {
        startCount++;
        const jobId = `job-${startCount}`;
        pollsByJob.set(jobId, 0);
        send(200, { job_id: jobId });
      } else if (req.url === "/status") {
        statusRequests++;
        const jobId = body.job_id as string;
        const seen = (pollsByJob.get(jobId) ?? 0) + 1;
        pollsByJob.set(jobId, seen);
        if (seen <= pendingPolls) send(200, { status: "pending" });
        else send(200, { status: "done", result });
      } else if (req.url === "/cancel") {
        cancelCount++;
        send(200, { cancelled: true });
      } else {
        send(404, {});
      }
    });
  });

  await new Promise<void>((r) => server.listen(0, r));
  return {
    port: (server.address() as AddressInfo).port,
    startCount: () => startCount,
    statusRequests: () => statusRequests,
    cancelCount: () => cancelCount,
    authHeaders: () => [...authSeen],
    stop: () => new Promise<void>((r) => server.close(() => r())),
  };
}

const AUTH_TOKEN = "s3cr3t-token";

// Apply the example definitions exactly as shipped — child before parent so the parent's
// child reference resolves at registration.
async function applyExample() {
  for (const def of [poller, parent]) {
    const { error } = await client.PUT("/definitions", { body: def as never });
    expect(error).toBeUndefined();
  }
}

async function startExample(port: number, extra: Record<string, unknown> = {}): Promise<string> {
  const { data, error } = await client.POST("/instances", {
    body: {
      process: parent.name,
      input: {
        jobs_url: `http://localhost:${port}`,
        headers: { Authorization: `Bearer ${AUTH_TOKEN}` },
        ...extra,
      },
    },
  });
  expect(error).toBeUndefined();
  return data!.id;
}

// The parent records its spawned child under context._children.<taskId>.<childKey>; here
// that's _children.run.poll. Poll until it appears and return the child instance id.
async function waitForChildId(parentId: string, timeoutMs = 10_000): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const { data } = await client.GET("/instances/{id}", { params: { path: { id: parentId } } });
    const childId = (data?.context as any)?._children?.run?.poll;
    if (typeof childId === "string") return childId;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`child of ${parentId} was not spawned within ${timeoutMs}ms`);
}

async function outputOf(id: string): Promise<any> {
  const { data } = await client.GET("/instances/{id}", { params: { path: { id } } });
  return (data?.context as any)?.output;
}

test("examples/polling-task: child polls a remote job until done and returns its result", async () => {
  const pendingPolls = 2; // two "pending" replies, then "done" on the third check
  const mock = await startJobService(pendingPolls, { answer: 42 }, `Bearer ${AUTH_TOKEN}`);

  try {
    await applyExample();
    const id = await startExample(mock.port, { poll_interval_ms: 50 });

    expect(await waitForInstance(id, 20_000)).toBe("completed");

    // The result polled out of the remote task made it all the way up to the parent.
    expect(await outputOf(id)).toEqual({ cancelled: false, timed_out: false, answer: 42 });

    // Started once, polled until done (pendingPolls "pending" + 1 "done"), never cancelled.
    expect(mock.startCount()).toBe(1);
    expect(mock.statusRequests()).toBe(pendingPolls + 1);
    expect(mock.cancelCount()).toBe(0);

    // Every request carried exactly the auth header the parent threaded down.
    expect(mock.authHeaders()).toEqual([`Bearer ${AUTH_TOKEN}`]);
  } finally {
    await mock.stop();
  }
});

test("examples/polling-task: a cancel signal during the wait stops the job and finishes", async () => {
  // The job never reports done, so the poller keeps looping until it's cancelled.
  const mock = await startJobService(Number.MAX_SAFE_INTEGER, { answer: 42 }, `Bearer ${AUTH_TOKEN}`);

  try {
    await applyExample();
    // High attempt budget + short interval so we cancel well before the timeout fires.
    const id = await startExample(mock.port, { poll_interval_ms: 50, max_attempts: 100 });

    // Signal the child's `wait` task to cancel. It buffers until the task next arms, so we
    // don't have to race the poll loop — the cancel is honoured on the next back-off.
    const childId = await waitForChildId(id);
    const { error: sigErr } = await client.POST("/instances/{id}/signal", {
      params: { path: { id: childId } },
      body: { task_id: "wait", result: { cancel: true } } as never,
    });
    expect(sigErr).toBeUndefined();

    expect(await waitForInstance(id, 20_000)).toBe("completed");

    // The process finished on the cancel path and reported it up to the parent.
    const output = await outputOf(id);
    expect(output.cancelled).toBe(true);
    expect(output.timed_out).toBe(false);

    // The job was started, polled at least once, and then cancelled on the server.
    expect(mock.startCount()).toBe(1);
    expect(mock.statusRequests()).toBeGreaterThanOrEqual(1);
    expect(mock.cancelCount()).toBe(1);
  } finally {
    await mock.stop();
  }
});

test("examples/polling-task: the poller gives up after max_attempts and cleans up", async () => {
  // The job never reports done; the caller caps it at two attempts.
  const mock = await startJobService(Number.MAX_SAFE_INTEGER, { answer: 42 }, `Bearer ${AUTH_TOKEN}`);

  try {
    await applyExample();
    const id = await startExample(mock.port, { poll_interval_ms: 50, max_attempts: 2 });

    expect(await waitForInstance(id, 20_000)).toBe("completed");

    // Reported as a timeout, not a cancel, with no answer.
    const output = await outputOf(id);
    expect(output.timed_out).toBe(true);
    expect(output.cancelled).toBe(false);
    expect(output.answer).toBe(0);

    // Exactly max_attempts status checks, then the server job was cleaned up.
    expect(mock.startCount()).toBe(1);
    expect(mock.statusRequests()).toBe(2);
    expect(mock.cancelCount()).toBe(1);
  } finally {
    await mock.stop();
  }
});
