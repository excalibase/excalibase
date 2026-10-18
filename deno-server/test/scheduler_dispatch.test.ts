// Phase 8 — scheduler dispatch tests.
//
// `ctx.scheduler.runAfter` / `runAt` / `cancel` post RPC messages from the
// worker; the main thread sends them to provisioning's
// /internal/runtime/projects/{projectId}/scheduler route, which writes the
// platform's scheduler tables as the scheduler's own login. The runtime never
// writes those tables: the project's login has no write path into the
// excalibase schema (EXC-623). A mutation's calls are sent only after it
// succeeds, so a mutation that throws schedules nothing.

import { assert, assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

interface SchedulerCall {
  projectId: string;
  token: string | null;
  ops: Array<Record<string, unknown>>;
}

interface MockProvisioning {
  url: string;
  calls: SchedulerCall[];
  stop: () => Promise<void>;
}

async function startMockProvisioning(status = 204): Promise<MockProvisioning> {
  const calls: SchedulerCall[] = [];
  const ac = new AbortController();
  let server!: Deno.HttpServer;
  const ready = new Promise<number>((resolve) => {
    server = Deno.serve({
      port: 0,
      signal: ac.signal,
      onListen({ port }) {
        resolve(port);
      },
    }, async (req) => {
      const match = new URL(req.url).pathname.match(/^\/internal\/runtime\/projects\/([^/]+)\/scheduler$/);
      if (match && req.method === "POST") {
        const body = await req.json() as { ops: Array<Record<string, unknown>> };
        calls.push({
          projectId: decodeURIComponent(match[1]!),
          token: req.headers.get("X-Excalibase-Runtime-Token"),
          ops: body.ops,
        });
        return new Response(status === 204 ? null : "refused", { status });
      }
      return new Response("not found", { status: 404 });
    });
  });
  const port = await ready;
  return {
    url: `http://127.0.0.1:${port}`,
    calls,
    stop: async () => {
      ac.abort();
      await server.finished;
    },
  };
}

// withRuntime runs body against a runtime wired to a mock provisioning.
async function withRuntime(
  body: (rt: Awaited<ReturnType<typeof startRuntime>>, prov: MockProvisioning) => Promise<void>,
  provisioningStatus = 204,
): Promise<void> {
  const prov = await startMockProvisioning(provisioningStatus);
  try {
    const rt = await startRuntime({ v2Enabled: true, provisioningUrl: prov.url });
    try {
      await body(rt, prov);
    } finally {
      await rt.stop();
    }
  } finally {
    await prov.stop();
  }
}

Deno.test({
  name: "ctx.scheduler.runAfter from a mutation is sent to provisioning after it succeeds",
  async fn() {
    await withRuntime(async (rt, prov) => {
      await rt.deploy("proj_sched__caller", bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, args) => {
          const id = await ctx.scheduler.runAfter(5000, { moduleName: "jobs", exportName: "send" }, { to: args.to });
          return { id };
        },
      }`));
      const before = Date.now();
      const res = await rt.invoke("proj_sched__caller", { args: { to: "ada@example.com" } });
      assertEquals(res.status, 200);
      const id = (JSON.parse(res.body) as { data: { id: string } }).data.id;
      assertEquals(prov.calls.length, 1);
      const call = prov.calls[0];
      assertEquals(call.projectId, "proj_sched");
      assert(call.token, "the call carries the runtime token");
      assertEquals(call.ops.length, 1);
      const op = call.ops[0];
      assertEquals(op.op, "schedule");
      assertEquals(op.id, id);
      assertEquals(op.moduleName, "jobs");
      assertEquals(op.exportName, "send");
      assertEquals(op.args, { to: "ada@example.com" });
      assert((op.scheduledForMs as number) >= before + 5000);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.scheduler.runAfter from a mutation that throws schedules nothing",
  async fn() {
    await withRuntime(async (rt, prov) => {
      await rt.deploy("proj_rollback__caller", bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          await ctx.scheduler.runAfter(5000, { moduleName: "jobs", exportName: "send" }, { to: "rollback@example.com" });
          throw new Error("rollback-after-schedule");
        },
      }`));
      await rt.invoke("proj_rollback__caller", { args: {} });
      assertEquals(prov.calls.length, 0, "a rolled-back mutation must not schedule");
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

// A nested mutation's schedule joins the outermost mutation: it is sent once
// the outer one succeeds, and dropped when the outer one throws.
Deno.test({
  name: "ctx.scheduler from a nested mutation waits for the outer mutation",
  async fn() {
    await withRuntime(async (rt, prov) => {
      await rt.deploy("proj_nest__inner", bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, args) => ctx.scheduler.runAfter(0, { moduleName: "jobs", exportName: "send" }, { n: args.n }),
      }`));
      await rt.deploy("proj_nest__outer", bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, args) => {
          await ctx.runMutation({ moduleName: "inner", exportName: "default" }, { n: args.n });
          if (args.fail) throw new Error("outer-fails");
          return { ok: true };
        },
      }`));
      await rt.invoke("proj_nest__outer", { args: { n: 1, fail: true } });
      assertEquals(prov.calls.length, 0, "the outer mutation threw, so nothing is scheduled");
      const res = await rt.invoke("proj_nest__outer", { args: { n: 2, fail: false } });
      assertEquals(res.status, 200, res.body);
      assertEquals(prov.calls.length, 1);
      assertEquals(prov.calls[0].ops.map((op) => op.args), [{ n: 2 }]);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.scheduler.runAfter from an action is sent regardless of the action's outcome",
  async fn() {
    await withRuntime(async (rt, prov) => {
      await rt.deploy("proj_action__caller", bundleDefault(`{
        kind: "action",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          await ctx.scheduler.runAfter(60_000, { moduleName: "jobs", exportName: "send" }, { to: "action@example.com" });
          throw new Error("post-schedule-action-fail");
        },
      }`));
      await rt.invoke("proj_action__caller", { args: {} });
      assertEquals(prov.calls.length, 1, "an action's schedule stands on its own");
      assertEquals(prov.calls[0].ops[0].args, { to: "action@example.com" });
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.scheduler.cancel in the same mutation is sent after the schedule it cancels",
  async fn() {
    await withRuntime(async (rt, prov) => {
      await rt.deploy("proj_cancel__caller", bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const id = await ctx.scheduler.runAfter(60_000, { moduleName: "jobs", exportName: "send" }, {});
          await ctx.scheduler.cancel(id);
          return { id };
        },
      }`));
      const res = await rt.invoke("proj_cancel__caller", { args: {} });
      const id = (JSON.parse(res.body) as { data: { id: string } }).data.id;
      assertEquals(prov.calls.length, 1, "one mutation, one call");
      assertEquals(prov.calls[0].ops.map((op) => op.op), ["schedule", "cancel"]);
      assertEquals(prov.calls[0].ops[1].id, id);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.scheduler.runAt sends the absolute millisecond timestamp",
  async fn() {
    await withRuntime(async (rt, prov) => {
      await rt.deploy("proj_runat__caller", bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, args) => {
          const id = await ctx.scheduler.runAt(args.at, { moduleName: "jobs", exportName: "ping" }, { mark: "at-test" });
          return { id };
        },
      }`));
      const at = Date.now() + 86_400_000;
      const res = await rt.invoke("proj_runat__caller", { args: { at } });
      assertEquals(res.status, 200);
      assertEquals(prov.calls[0].ops[0].scheduledForMs, at);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "an action whose schedule cannot reach provisioning never leaks the URL",
  async fn() {
    // A provisioning URL that nothing answers, so the fetch fails at the
    // network level — the path that used to carry the URL into the error.
    const deadURL = "http://127.0.0.1:1";
    const rt = await startRuntime({ v2Enabled: true, provisioningUrl: deadURL });
    try {
      await rt.deploy("proj_leak__caller", bundleDefault(`{
        kind: "action",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          try {
            await ctx.scheduler.runAfter(1000, { moduleName: "jobs", exportName: "send" }, {});
            return { scheduled: true };
          } catch (err) {
            return { error: String(err && err.message ? err.message : err) };
          }
        },
      }`));
      const res = await rt.invoke("proj_leak__caller", { args: {} });
      assert(!res.body.includes("127.0.0.1:1"), `error leaked the URL: ${res.body}`);
      assert(!res.body.includes("http://"), `error leaked a URL: ${res.body}`);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "a mutation whose schedule provisioning refuses reports an error, not success",
  async fn() {
    await withRuntime(async (rt, prov) => {
      await rt.deploy("proj_refused__caller", bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const id = await ctx.scheduler.runAfter(1000, { moduleName: "jobs", exportName: "send" }, {});
          return { id };
        },
      }`));
      const res = await rt.invoke("proj_refused__caller", { args: {} });
      assertEquals(prov.calls.length, 1);
      assertEquals(res.status, 500);
      assert(res.body.includes("scheduled functions were not recorded"), res.body);
      // The caller must not learn the internal provisioning endpoint.
      assert(!res.body.includes("http"), `error body leaked a URL: ${res.body}`);
      assert(!prov.url.includes("127.0.0.1") || !res.body.includes("127.0.0.1"), res.body);
    }, 500);
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

// Validation and configuration errors: an action's ctx.scheduler call that is
// malformed, or made with no provisioning URL configured, is rejected before
// anything is sent. No Postgres is needed — actions open no transaction.
Deno.test({
  name: "ctx.scheduler rejects malformed calls and a missing provisioning URL",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const cases: Array<[string, string]> = [
        ["missing ref", `await ctx.scheduler.runAfter(1000, undefined, {})`],
        ["bad time", `await ctx.scheduler.runAt(NaN, { moduleName: "jobs", exportName: "send" }, {})`],
        ["empty cancel id", `await ctx.scheduler.cancel("")`],
        ["not configured", `await ctx.scheduler.runAfter(1000, { moduleName: "jobs", exportName: "send" }, {})`],
      ];
      for (const [name, call] of cases) {
        const id = `proj_badsched_${name.replace(/[^a-z]/g, "")}__caller`;
        await rt.deploy(id, bundleDefault(`{
          kind: "action",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            try { ${call}; return { ok: true }; }
            catch (err) { return { error: String(err && err.message ? err.message : err) }; }
          },
        }`));
        const res = await rt.invoke(id, { args: {} });
        const parsed = JSON.parse(res.body) as { data?: { ok?: boolean; error?: string } };
        if (!parsed.data || !parsed.data.error) {
          throw new Error(`${name}: expected a rejection, got ${res.body}`);
        }
      }
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
