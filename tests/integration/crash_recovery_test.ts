import { expect, test, beforeAll, afterAll } from "vitest";
import { join } from "path";
import { tmpdir } from "os";
import { spawnSync } from "child_process";
import { buildGenrocBinary, startGenroc } from "../helpers/server.ts";
import { startMockService, waitForInstance } from "../helpers/client.ts";

// The sqlite and postgres vitest projects run this file in parallel, and both read
// the global POSTGRES_DSN, so offset the (otherwise fixed) genroc ports per project
// to keep their own genroc1/genroc2 processes from colliding.
const PORT_OFFSET = (Number(process.env.GENROC_PORT ?? 8888) - 8888) * 4;
const GENROC1_PORT = 20011 + PORT_OFFSET;
const GENROC2_PORT = 20012 + PORT_OFFSET;
// Second pair, for the pause-crash tests below (they run in the same file but must
// not reuse the ports above while those servers are still shutting down).
const PAUSE1_PORT = 20061 + PORT_OFFSET;
const PAUSE2_PORT = 20062 + PORT_OFFSET;

let genrocBin: string;
let crashPgDSN: string | undefined;
let tempDbName: string | undefined;

function replaceDbName(dsn: string, dbName: string): string {
  const url = new URL(dsn);
  url.pathname = `/${dbName}`;
  return url.toString();
}

beforeAll(async () => {
  genrocBin = await buildGenrocBinary();

  const rawDsn = process.env.POSTGRES_DSN;
  if (rawDsn) {
    tempDbName = `genroc_crash_${Date.now()}`;
    const adminDsn = replaceDbName(rawDsn, "postgres");
    const result = spawnSync(
      "psql",
      [adminDsn, "-c", `CREATE DATABASE ${tempDbName}`],
      {
        stdio: "pipe",
      },
    );
    if (result.status !== 0) {
      throw new Error(
        `Failed to create crash recovery database: ${result.stderr.toString()}`,
      );
    }
    crashPgDSN = replaceDbName(rawDsn, tempDbName);
  }
}, 120_000);

afterAll(() => {
  if (tempDbName) {
    const adminDsn = replaceDbName(process.env.POSTGRES_DSN!, "postgres");
    spawnSync(
      "psql",
      [adminDsn, "-c", `DROP DATABASE ${tempDbName} WITH (FORCE)`],
      { stdio: "pipe" },
    );
  }
});

test("crash recovery — new worker re-executes an unconfirmed task after the previous worker crashes", async () => {
  const db = crashPgDSN ? "" : join(tmpdir(), `genroc_crash_${Date.now()}.db`);

  // firstRequestDelayMs: Infinity keeps the connection open so the task
  // stays in-flight when we crash the worker.
  const mock = await startMockService(0, {
    response: { done: true },
    firstRequestDelayMs: Infinity,
  });

  const genroc1 = await startGenroc(genrocBin, GENROC1_PORT, db, crashPgDSN);
  try {
    const processName = `crash_recovery_${crypto.randomUUID()}`;
    await genroc1.client.PUT("/definitions", {
      body: {
        name: processName,

        tasks: [
          {
            id: "work",
            action: {
              type: "fetch" as const,
              url: `http://localhost:${mock.port}/action`,
            },
            // Long enough that the task never times out before the crash.
            timeout_ms: 120_000,
            switch: [{ goto: "end" }],
          },
        ],
      },
    });

    const { data: startData } = await genroc1.client.POST("/instances", {
      body: { process: processName },
    });
    const instanceId = startData!.id;

    // Wait until genroc1 has claimed the instance and the task is in-flight.
    await Promise.race([
      mock.firstRequestReceived,
      new Promise<never>((_, reject) =>
        setTimeout(
          () => reject(new Error("mock never received first request")),
          15_000,
        ),
      ),
    ]);

    // Crash: SIGKILL leaves the lease in the database without releasing it.
    genroc1.crash();

    // Manual-tick mode (--poll 0): /tick is only available when the continuous
    // pump is off, and it lets us drive reclaim deterministically.
    const genroc2 = await startGenroc(genrocBin, GENROC2_PORT, db, crashPgDSN, 0);
    // The engine lease is 10 s. Instead of waiting it out, shift genroc2's
    // clock forward so genroc1's lease is already expired from its view,
    // and tick immediately so it reclaims the instance.
    await genroc2.client.POST("/tick", { body: { advance_ms: 12_000 } });
    try {
      const finalStatus = await waitForInstance(
        instanceId,
        15_000,
        genroc2.client,
      );

      // genroc2 must have re-executed the task and completed the instance.
      expect(finalStatus).toBe("completed");
      // Once by genroc1 (abandoned at crash), once by genroc2 (confirmed).
      expect(mock.requestCount()).toBe(2);
    } finally {
      genroc2.stop();
    }
  } finally {
    genroc1.crash(); // no-op if already dead
    await mock.stop();
  }
}, 60_000);

test("crash recovery — an only_once task is failed (not re-executed) after a lease takeover", async () => {
  const db = crashPgDSN ? "" : join(tmpdir(), `genroc_crash_once_${Date.now()}.db`);

  // The first request hangs so the task is in-flight when we crash the worker.
  const mock = await startMockService(0, {
    response: { done: true },
    firstRequestDelayMs: Infinity,
  });

  const genroc1 = await startGenroc(genrocBin, GENROC1_PORT, db, crashPgDSN);
  try {
    const processName = `crash_only_once_${crypto.randomUUID()}`;
    await genroc1.client.PUT("/definitions", {
      body: {
        name: processName,
        tasks: [
          {
            id: "work",
            action: {
              type: "fetch" as const,
              url: `http://localhost:${mock.port}/action`,
            },
            // only_once: the engine must not re-run this on a lease takeover, since
            // the call may already have happened on the crashed worker.
            only_once: true,
            timeout_ms: 120_000,
            switch: [{ goto: "end" }],
          },
        ],
      },
    });

    const { data: startData } = await genroc1.client.POST("/instances", {
      body: { process: processName },
    });
    const instanceId = startData!.id;

    // Wait until genroc1 has claimed the instance and the task is in-flight.
    await Promise.race([
      mock.firstRequestReceived,
      new Promise<never>((_, reject) =>
        setTimeout(
          () => reject(new Error("mock never received first request")),
          15_000,
        ),
      ),
    ]);

    genroc1.crash();

    const genroc2 = await startGenroc(genrocBin, GENROC2_PORT, db, crashPgDSN, 0);
    await genroc2.client.POST("/tick", { body: { advance_ms: 12_000 } });
    try {
      const finalStatus = await waitForInstance(
        instanceId,
        15_000,
        genroc2.client,
      );

      // genroc2 detected the takeover and refused to re-execute the only_once task.
      expect(finalStatus).toBe("failed");
      const { data } = await genroc2.client.GET("/instances/{id}", {
        params: { path: { id: instanceId } },
      });
      expect(data!.error).toContain("only_once");
      // Only genroc1's abandoned attempt — genroc2 never sent the request.
      expect(mock.requestCount()).toBe(1);
    } finally {
      genroc2.stop();
    }
  } finally {
    genroc1.crash();
    await mock.stop();
  }
}, 60_000);

// The one path that only a crash can reach.
//
// A pause normally lands in SQL: the worker holding the instance writes its finished
// task and the CASE in UpdateInstance turns 'pausing' into 'paused'. If that worker
// dies first, the row is stranded leased-but-dead in 'pausing', and only a reclaiming
// worker can settle it — which is the entire reason 'pausing' stays in the claim
// predicate. This drives that deterministically: hold the task open, pause, SIGKILL,
// then let a second worker reclaim.
async function pauseThenCrash(
  processName: string,
  db: string,
  mockPort: number,
  onlyOnce: boolean,
) {
  const genroc1 = await startGenroc(genrocBin, PAUSE1_PORT, db, crashPgDSN);
  await genroc1.client.PUT("/definitions", {
    body: {
      name: processName,
      tasks: [
        {
          id: "work",
          ...(onlyOnce ? { only_once: true } : {}),
          action: {
            type: "fetch" as const,
            url: `http://localhost:${mockPort}/action`,
          },
          timeout_ms: 120_000,
          switch: [{ goto: "end" }],
        },
      ],
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
    } as any,
  });
  const { data: startData } = await genroc1.client.POST("/instances", {
    body: { process: processName },
  });
  return { genroc1, instanceId: startData!.id };
}

test("a pausing instance whose worker crashes is settled to paused by the reclaimer", async () => {
  const db = crashPgDSN ? "" : join(tmpdir(), `genroc_pause_crash_${Date.now()}.db`);
  const mock = await startMockService(0, {
    response: { done: true },
    firstRequestDelayMs: Infinity,
  });
  const name = `pause_crash_${crypto.randomUUID()}`;
  const { genroc1, instanceId } = await pauseThenCrash(name, db, mock.port, false);

  try {
    // Wait until the task is genuinely in flight, so the row is leased.
    await mock.firstRequestReceived;

    // Leased, so the pause can only be recorded as a request: the worker is
    // mid-task and cannot be stopped.
    await genroc1.client.POST("/instances/{id}/pause", {
      params: { path: { id: instanceId } },
    });
    const { data: mid } = await genroc1.client.GET("/instances/{id}", {
      params: { path: { id: instanceId } },
    });
    expect(mid!.status).toBe("pausing");

    // Crash before the worker can land the pause.
    genroc1.crash();

    const genroc2 = await startGenroc(genrocBin, PAUSE2_PORT, db, crashPgDSN, 0);
    try {
      // Expire the dead lease from genroc2's view and let it reclaim.
      await genroc2.client.POST("/tick", { body: { advance_ms: 12_000 } });

      const { data: after } = await genroc2.client.GET("/instances/{id}", {
        params: { path: { id: instanceId } },
      });
      // Settled, not advanced: the pause the operator asked for is honoured, and
      // the abandoned task is NOT re-executed on the way.
      expect(after!.status).toBe("paused");
      expect(mock.requestCount()).toBe(1);

      // This is the one case where the deferred landing is audited, because it
      // went through the engine rather than a worker's own write. Audit rows are
      // buffered and flushed on a 5ms ticker, so poll rather than read once.
      let events: string[] = [];
      for (let i = 0; i < 40 && !events.includes("inst_paused"); i++) {
        const { data: logs } = await genroc2.client.GET("/instances/{id}/logs", {
          params: { path: { id: instanceId }, query: { limit: 100 } },
        });
        events = (logs!.items ?? []).map((l) => l.event as string);
        if (!events.includes("inst_paused")) await new Promise((r) => setTimeout(r, 50));
      }
      expect(events).toContain("inst_paused");

      // And it resumes normally from there — the task runs on the next tick.
      await genroc2.client.POST("/instances/{id}/resume", {
        params: { path: { id: instanceId } },
      });
      await genroc2.client.POST("/tick", {});
      expect(mock.requestCount()).toBe(2);
    } finally {
      genroc2.stop();
    }
  } finally {
    genroc1.crash(); // no-op if already dead
    await mock.stop();
  }
}, 60_000);

test("a pausing only_once instance whose worker crashes fails instead of pausing", async () => {
  const db = crashPgDSN ? "" : join(tmpdir(), `genroc_pause_once_${Date.now()}.db`);
  const mock = await startMockService(0, {
    response: { done: true },
    firstRequestDelayMs: Infinity,
  });
  const name = `pause_once_${crypto.randomUUID()}`;
  const { genroc1, instanceId } = await pauseThenCrash(name, db, mock.port, true);

  try {
    await mock.firstRequestReceived;
    await genroc1.client.POST("/instances/{id}/pause", {
      params: { path: { id: instanceId } },
    });
    genroc1.crash();

    const genroc2 = await startGenroc(genrocBin, PAUSE2_PORT, db, crashPgDSN, 0);
    try {
      await genroc2.client.POST("/tick", { body: { advance_ms: 12_000 } });

      // The interrupted call may already have happened on the dead worker, so
      // pausing here would launder an at-most-once violation into a silent
      // re-execution on resume. The instance fails instead — and stays failed,
      // because a failure is an outcome and a pause is not.
      const { data: after } = await genroc2.client.GET("/instances/{id}", {
        params: { path: { id: instanceId } },
      });
      expect(after!.status).toBe("failed");
      expect(after!.error).toContain("only_once");
      expect(mock.requestCount()).toBe(1);
    } finally {
      genroc2.stop();
    }
  } finally {
    genroc1.crash();
    await mock.stop();
  }
}, 60_000);
