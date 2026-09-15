// /health reports a per-process bootId (EXC-337). Provisioning compares it
// across polls to detect a runtime restart and replay the project's functions
// from the control-plane store, so the id must be stable within a process and
// different across processes.

import {
  assertEquals,
  assertMatch,
  assertNotEquals,
} from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

interface Health {
  status: string;
  scripts: number;
  bootId: string;
}

async function health(baseUrl: string): Promise<Health> {
  const res = await fetch(`${baseUrl}/health`);
  return await res.json() as Health;
}

const UUID_V4 =
  /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

// The harness's stop() leaves its grace-period timer pending, as in the
// sibling runtime tests, so the op/resource sanitizers are off here too.
Deno.test({
  name: "health exposes a stable bootId for the life of the process",
  sanitizeOps: false,
  sanitizeResources: false,
  async fn() {
    const rt = await startRuntime();
    try {
      const first = await health(rt.baseUrl);
      assertEquals(first.status, "healthy");
      assertMatch(first.bootId, UUID_V4);

      const deployed = await rt.deploy(
        "boot-fn",
        "export default () => new Response('ok')",
      );
      await deployed.body?.cancel();

      const second = await health(rt.baseUrl);
      assertEquals(second.bootId, first.bootId);
      assertEquals(second.scripts, 1);
    } finally {
      await rt.stop();
    }
  },
});

Deno.test({
  name: "a restarted runtime reports a different bootId and zero scripts",
  sanitizeOps: false,
  sanitizeResources: false,
  async fn() {
    const first = await startRuntime();
    let firstBootId: string;
    try {
      const deployed = await first.deploy(
        "boot-fn",
        "export default () => new Response('ok')",
      );
      await deployed.body?.cancel();
      firstBootId = (await health(first.baseUrl)).bootId;
    } finally {
      await first.stop();
    }

    const second = await startRuntime();
    try {
      const after = await health(second.baseUrl);
      assertNotEquals(after.bootId, firstBootId);
      assertEquals(after.scripts, 0);
    } finally {
      await second.stop();
    }
  },
});
