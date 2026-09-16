// Phase 8 — scheduler dispatch tests.
//
// `ctx.scheduler.runAfter` / `runAt` / `cancel` post RPC messages from the
// worker; the main thread translates them into INSERT/UPDATE statements on
// `excalibase.excalibase_scheduled_functions` in the project database —
// the reserved schema, never the tenant's user-facing `public`. Calling the
// scheduler from a mutation must respect the mutation's transactional
// boundary — a rollback unrolls the insert too.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { startPostgres } from "./pg_harness.ts";

function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

async function ensureSchedulerTables(pgUrl: string): Promise<void> {
  const postgres = (await import("npm:postgres@3.4.4")).default;
  const sql = postgres(pgUrl, { onnotice: () => {} });
  try {
    await sql.unsafe(`
      CREATE SCHEMA IF NOT EXISTS excalibase;
      CREATE TABLE IF NOT EXISTS excalibase.excalibase_scheduled_functions (
        id text PRIMARY KEY,
        project_id text NOT NULL,
        module_name text NOT NULL,
        export_name text NOT NULL,
        args jsonb NOT NULL,
        scheduled_for timestamptz NOT NULL,
        status text NOT NULL DEFAULT 'pending',
        attempts int NOT NULL DEFAULT 0,
        last_error text,
        created_at timestamptz NOT NULL DEFAULT now()
      );
    `);
  } finally {
    await sql.end({ timeout: 1 });
  }
}

async function queryScheduled(
  pgUrl: string,
  where: string,
  ...params: unknown[]
): Promise<Array<Record<string, unknown>>> {
  const postgres = (await import("npm:postgres@3.4.4")).default;
  const sql = postgres(pgUrl, { onnotice: () => {} });
  try {
    // postgres.js .unsafe params is typed against the `Sql` template
    // generic; cast through `any` so heterogeneous values pass cleanly.
    // deno-lint-ignore no-explicit-any
    const rows = await (sql as any).unsafe(
      `SELECT id, project_id, module_name, export_name, status, args
       FROM excalibase.excalibase_scheduled_functions WHERE ${where}`,
      params,
    );
    return rows as unknown as Array<Record<string, unknown>>;
  } finally {
    await sql.end({ timeout: 1 });
  }
}

Deno.test({
  name: "ctx.scheduler.runAfter from a mutation inserts a pending row",
  async fn() {
    const pg = await startPostgres();
    try {
      await ensureSchedulerTables(pg.url);
      const rt = await startRuntime({ v2Enabled: true, dbUrl: pg.url });
      try {
        const callerCode = bundleDefault(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const id = await ctx.scheduler.runAfter(
              5000,
              { moduleName: "jobs", exportName: "send" },
              { to: args.to }
            );
            return { id };
          },
        }`);
        await rt.deploy("proj_sched__caller", callerCode);
        const res = await rt.invoke("proj_sched__caller", { args: { to: "ada@example.com" } });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body) as { data: { id: string } };
        const idResult = parsed.data.id;
        const rows = await queryScheduled(pg.url, "id = $1", idResult);
        assertEquals(rows.length, 1);
        assertEquals(rows[0].project_id, "proj_sched");
        assertEquals(rows[0].module_name, "jobs");
        assertEquals(rows[0].export_name, "send");
        assertEquals(rows[0].status, "pending");
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.scheduler.runAfter from a mutation rolls back when the handler throws",
  async fn() {
    const pg = await startPostgres();
    try {
      await ensureSchedulerTables(pg.url);
      const rt = await startRuntime({ v2Enabled: true, dbUrl: pg.url });
      try {
        const callerCode = bundleDefault(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            await ctx.scheduler.runAfter(
              5000,
              { moduleName: "jobs", exportName: "send" },
              { to: "rollback@example.com" }
            );
            throw new Error("rollback-after-schedule");
          },
        }`);
        await rt.deploy("proj_rollback__caller", callerCode);
        await rt.invoke("proj_rollback__caller", { args: {} });
        // The mutation throws; transactional roll-back must purge the insert.
        const rows = await queryScheduled(
          pg.url,
          "project_id = $1 AND args->>'to' = $2",
          "proj_rollback",
          "rollback@example.com",
        );
        assertEquals(rows.length, 0, `expected 0 rows, got ${rows.length}`);
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.scheduler.runAfter from an action commits regardless of action outcome",
  async fn() {
    const pg = await startPostgres();
    try {
      await ensureSchedulerTables(pg.url);
      const rt = await startRuntime({ v2Enabled: true, dbUrl: pg.url });
      try {
        const callerCode = bundleDefault(`{
          kind: "action",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const id = await ctx.scheduler.runAfter(
              60_000,
              { moduleName: "jobs", exportName: "send" },
              { to: "action@example.com" }
            );
            // Action throws after scheduling — but Convex parity says the
            // schedule still commits because actions have no transactional
            // boundary on the scheduler write.
            throw new Error("post-schedule-action-fail");
          },
        }`);
        await rt.deploy("proj_action__caller", callerCode);
        await rt.invoke("proj_action__caller", { args: {} });
        const rows = await queryScheduled(
          pg.url,
          "project_id = $1 AND args->>'to' = $2",
          "proj_action",
          "action@example.com",
        );
        assertEquals(rows.length, 1, "action-side schedule must commit");
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.scheduler.cancel marks a pending row cancelled",
  async fn() {
    const pg = await startPostgres();
    try {
      await ensureSchedulerTables(pg.url);
      const rt = await startRuntime({ v2Enabled: true, dbUrl: pg.url });
      try {
        const callerCode = bundleDefault(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const id = await ctx.scheduler.runAfter(
              60_000,
              { moduleName: "jobs", exportName: "send" },
              {}
            );
            await ctx.scheduler.cancel(id);
            return { id };
          },
        }`);
        await rt.deploy("proj_cancel__caller", callerCode);
        const res = await rt.invoke("proj_cancel__caller", { args: {} });
        const parsed = JSON.parse(res.body) as { data: { id: string } };
        const rows = await queryScheduled(pg.url, "id = $1", parsed.data.id);
        assertEquals(rows.length, 1);
        assertEquals(rows[0].status, "cancelled");
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.scheduler.runAt accepts an absolute millisecond timestamp",
  async fn() {
    const pg = await startPostgres();
    try {
      await ensureSchedulerTables(pg.url);
      const rt = await startRuntime({ v2Enabled: true, dbUrl: pg.url });
      try {
        const callerCode = bundleDefault(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const id = await ctx.scheduler.runAt(
              args.at,
              { moduleName: "jobs", exportName: "ping" },
              { mark: "at-test" }
            );
            return { id };
          },
        }`);
        await rt.deploy("proj_runat__caller", callerCode);
        const at = Date.now() + 86_400_000;
        const res = await rt.invoke("proj_runat__caller", { args: { at } });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body) as { data: { id: string } };
        const rows = await queryScheduled(pg.url, "id = $1", parsed.data.id);
        assertEquals(rows.length, 1);
        assertEquals(rows[0].status, "pending");
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
