/**
 * Tests that observe how errors interact with cancellation in a 3-level process tree:
 *
 *   grandparent
 *     └─ parent  (child call)
 *          ├─ a  (child_parallel)  ← always calls failWorker → HTTP 500 → fails
 *          └─ b  (child_parallel)  ← calls successWorker → HTTP 200 → completes
 *
 * Key invariants:
 *   - Errors take precedence over cancellation: FailAncestors marks ancestors
 *     as 'failing' even if they were 'cancelling waiting'.
 *   - Ancestors drain through 'failing' (keeping wait_state) and settle to
 *     'failed' one level per tick, bottom-up — a root is 'failed' only once
 *     its whole tree is inactive, which is what makes it retryable.
 *
 * Same server/tick/ordering conventions as tree_cancel_test.ts; see that file for details.
 *
 * buildTree() leaves the tree at:
 *   gp="running waiting", parent="running waiting", a="running", b="running"
 */
import { expect, test, beforeAll, afterAll } from "vitest";
import { startMockService } from "../helpers/client.ts";
import { useTickEnv } from "./helpers.ts";

const PORT = 20015;
const ctx = useTickEnv(PORT);

let failMockPort: number;
let successMockPort: number;
let stopMocks: (() => Promise<void>) | undefined;
let failWorkerName: string;
let successWorkerName: string;
let parentName: string;
let gpName: string;

beforeAll(async () => {
  const uid = crypto.randomUUID().slice(0, 8);
  failWorkerName = `fail_worker_${uid}`;
  successWorkerName = `success_worker_${uid}`;
  parentName = `parent_${uid}`;
  gpName = `gp_${uid}`;

  const failMock = await startMockService(0, { statusCode: 500 });
  const successMock = await startMockService(0, { response: { ok: true } });
  failMockPort = failMock.port;
  successMockPort = successMock.port;
  stopMocks = async () => {
    await failMock.stop();
    await successMock.stop();
  };

  await ctx.env.define(failWorkerName, [
    {
      id: "work",
      call: {
        type: "rest" as const,
        endpoint: `http://localhost:${failMockPort}/action`,
      },
      timeout_ms: 5_000,
      switch: [{ goto: "end" }],
    },
  ]);

  await ctx.env.define(successWorkerName, [
    {
      id: "work",
      call: {
        type: "rest" as const,
        endpoint: `http://localhost:${successMockPort}/action`,
      },
      timeout_ms: 5_000,
      switch: [{ goto: "end" }],
    },
  ]);

  await ctx.env.define(parentName, [
    {
      id: "run_children",
      call: {
        type: "child_parallel" as const,
        children: {
          a: { name: failWorkerName },
          b: { name: successWorkerName },
        },
      },
      switch: [{ goto: "end" }],
    },
  ]);

  await ctx.env.define(gpName, [
    {
      id: "run_parent",
      call: { type: "child" as const, name: parentName },
      switch: [{ goto: "end" }],
    },
  ]);
}, 60_000);

afterAll(() => stopMocks?.());

// Builds the full tree and leaves it at:
//   gp="running waiting", parent="running waiting", a="running", b="running"
async function buildTree() {
  const gp = await ctx.env.start(gpName);

  // tick: gp spawns parent → gp transitions to running+wait_state=waiting
  await ctx.env.tick();
  const parent = await ctx.env.childOf(gp, "run_parent");

  // tick: parent spawns a and b → parent transitions to running+wait_state=waiting
  await ctx.env.tick();
  const { a, b } = await ctx.env.childrenOf(parent, "run_children");

  expect(await ctx.env.statuses({ gp, parent, a, b })).toEqual({
    gp: "running waiting",
    parent: "running waiting",
    a: "running",
    b: "running",
  });

  return { gp, parent, a, b };
}

test("a fails — ancestors drain through 'failing' and settle to 'failed' one level per tick", async () => {
  const { gp, parent, a, b } = await buildTree();
  try {
    // tick: a (smaller created_at) is claimed and executed; its REST call returns 500.
    // failInstance(a) → FailInstanceAndAncestors: a is failed (terminal), parent
    // and gp become 'failing' but keep wait_state='waiting' — b is still active.
    await ctx.env.tick();
    expect(await ctx.env.statuses({ gp, parent, a, b })).toEqual({
      gp: "failing waiting",
      parent: "failing waiting",
      a: "failed",
      b: "running",
    });

    // tick: b runs and completes normally. FinishChild(b): all batch children
    // terminal → parent woken to 'collecting' (now claimable).
    await ctx.env.tick();
    expect(await ctx.env.statuses({ gp, parent, a, b })).toEqual({
      gp: "failing waiting",
      parent: "failing collecting",
      a: "failed",
      b: "completed",
    });

    // tick: parent (failing, claimable) settles to 'failed'; its terminal save
    // wakes gp to 'collecting'.
    await ctx.env.tick();
    expect(await ctx.env.statuses({ gp, parent, a, b })).toEqual({
      gp: "failing collecting",
      parent: "failed",
      a: "failed",
      b: "completed",
    });

    // tick: gp settles to 'failed' — the root is terminal only now that the
    // whole tree is inactive, so 'failed' means retryable.
    await ctx.env.tick();
    expect(await ctx.env.statuses({ gp, parent, a, b })).toEqual({
      gp: "failed",
      parent: "failed",
      a: "failed",
      b: "completed",
    });
  } finally {
    await ctx.env.tickUntilIdle();
  }
});

test("a fails while ancestors are cancelling — FailAncestors overrides 'cancelling'; cancelled sibling leaves parent failed", async () => {
  // A separate tree whose failing worker holds its first request open, so the
  // root can be cancelled while a's call is still in flight. The failure then
  // lands on 'cancelling' ancestors and must override them to 'failed'.
  const uid = crypto.randomUUID().slice(0, 8);
  const holdMock = await startMockService(0, {
    statusCode: 500,
    firstRequestDelayMs: Infinity,
  });
  try {
    const holdWorker = `hold_worker_${uid}`;
    await ctx.env.define(holdWorker, [
      {
        id: "work",
        call: {
          type: "rest" as const,
          endpoint: `http://localhost:${holdMock.port}/action`,
        },
        timeout_ms: 5_000,
        switch: [{ goto: "end" }],
      },
    ]);
    const parent2Name = `parent2_${uid}`;
    await ctx.env.define(parent2Name, [
      {
        id: "run_children",
        call: {
          type: "child_parallel" as const,
          children: {
            a: { name: holdWorker },
            b: { name: successWorkerName },
          },
        },
        switch: [{ goto: "end" }],
      },
    ]);
    const gp2Name = `gp2_${uid}`;
    await ctx.env.define(gp2Name, [
      {
        id: "run_parent",
        call: { type: "child" as const, name: parent2Name },
        switch: [{ goto: "end" }],
      },
    ]);

    const gp = await ctx.env.start(gp2Name);
    await ctx.env.tick();
    const parent = await ctx.env.childOf(gp, "run_parent");
    await ctx.env.tick();
    const { a, b } = await ctx.env.childrenOf(parent, "run_children");

    // Tick 3 claims a (spawned first); its call hangs on the held mock.
    const tickPromise = ctx.env.tick();
    await holdMock.firstRequestReceived;

    // Cancel the root while a's call is in flight — the whole tree (including
    // the claimed a, whose DB row is still 'running') becomes 'cancelling'.
    await ctx.env.cancel(gp);

    // Release the held 500. The engine still holds a as in-memory 'running',
    // so the failure path runs: failInstance(a) → FailAncestors:
    // WHERE status IN ('running', 'cancelling') — the cancelling ancestors are
    // overridden to 'failing' (error wins), keeping wait_state='waiting'.
    holdMock.release();
    await tickPromise;

    expect(await ctx.env.statuses({ gp, parent, a, b })).toEqual({
      gp: "failing waiting",
      parent: "failing waiting",
      a: "failed",
      b: "cancelling",
    });

    // The root is draining ('failing') while b settles — a retry is rejected
    // by the status check alone.
    const { error: earlyRetryErr } = await ctx.env.client.POST(
      "/instances/{id}/retry",
      { params: { path: { id: gp } } },
    );
    expect(earlyRetryErr).toBeDefined();
    expect(JSON.stringify(earlyRetryErr)).toContain("not retryable");

    // tick: b (cancelling) is processed → cancelInstance → cancelled.
    // FinishChild(b): all batch children terminal → parent woken to collecting.
    await ctx.env.tick();
    expect(await ctx.env.statuses({ gp, parent, a, b })).toEqual({
      gp: "failing waiting",
      parent: "failing collecting",
      a: "failed",
      b: "cancelled",
    });

    // tick: parent settles failing → failed (error wins over the cancel);
    // its terminal save wakes gp.
    await ctx.env.tick();
    expect(await ctx.env.statuses({ gp, parent, a, b })).toEqual({
      gp: "failing collecting",
      parent: "failed",
      a: "failed",
      b: "cancelled",
    });

    // tick: gp settles failing → failed — only now is the root retryable.
    await ctx.env.tick();
    expect(await ctx.env.statuses({ gp, parent, a, b })).toEqual({
      gp: "failed",
      parent: "failed",
      a: "failed",
      b: "cancelled",
    });

    // The error from 'a' is propagated to ancestors via FailAncestors.
    const { data: parentInst } = await ctx.env.client.GET("/instances/{id}", {
      params: { path: { id: parent } },
    });
    expect(parentInst?.error).toBeTruthy();
  } finally {
    await ctx.env.tickUntilIdle();
    await holdMock.stop();
  }
});
