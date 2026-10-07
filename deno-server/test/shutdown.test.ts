// EXC-569: a runtime pod told to stop keeps serving for a moment while the
// cluster takes it out of the Service, and says it is going on /health so
// readiness drops it sooner. Exiting at once left calls that were still
// routed to it refused.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { drainThenExit, shutdownDrainMs } from "../runtime/shutdown.ts";
import { startRuntime } from "./harness.ts";

Deno.test("the drain defaults to a few seconds and is bounded", () => {
  assertEquals(shutdownDrainMs(undefined), 5000);
  assertEquals(shutdownDrainMs(""), 5000);
  assertEquals(shutdownDrainMs("not a number"), 5000);
  assertEquals(shutdownDrainMs("-1"), 5000);
  assertEquals(shutdownDrainMs("0"), 0);
  assertEquals(shutdownDrainMs("1500"), 1500);
  assertEquals(shutdownDrainMs("600000"), 20000);
});

Deno.test("a stop marks the runtime draining, waits, then stops serving and exits once", async () => {
  const steps: string[] = [];
  const stop = drainThenExit({
    drainMs: 30,
    markDraining: () => steps.push("draining"),
    sleep: (ms) => {
      steps.push(`sleep ${ms}`);
      return Promise.resolve();
    },
    stopServing: () => {
      steps.push("stop serving");
      return Promise.resolve();
    },
    closePool: () => {
      steps.push("close pool");
      return Promise.resolve();
    },
    exit: (code) => steps.push(`exit ${code}`),
  });
  await Promise.all([stop(), stop()]);
  assertEquals(steps, ["draining", "sleep 30", "stop serving", "close pool", "exit 0"]);
});

Deno.test("a failing step does not keep the runtime from exiting", async () => {
  const steps: string[] = [];
  const stop = drainThenExit({
    drainMs: 0,
    markDraining: () => steps.push("draining"),
    sleep: () => Promise.resolve(),
    stopServing: () => Promise.reject(new Error("already closed")),
    closePool: () => Promise.reject(new Error("pool gone")),
    exit: (code) => steps.push(`exit ${code}`),
  });
  await stop();
  assertEquals(steps, ["draining", "exit 0"]);
});

Deno.test({
  name: "SIGTERM: health says draining, calls are still served, then the process exits",
  sanitizeOps: false,
  sanitizeResources: false,
  async fn() {
    const rt = await startRuntime({ shutdownDrainMs: 1500 });
    try {
      rt.signal("SIGTERM");
      await delay(200);

      const health = await rt.raw("/health");
      assertEquals(health.status, 503);
      assertEquals((await health.json()).status, "draining");

      const deployed = await rt.deploy("late", "export default () => new Response('ok')");
      await deployed.body?.cancel();
      assertEquals(deployed.ok, true);

      assertEquals(await rt.exited(5000), true);
    } finally {
      await rt.stop();
    }
  },
});
