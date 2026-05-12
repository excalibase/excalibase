// NATS test harness — shells out to `docker run -d nats:2-alpine`, polls a
// random host port until the NATS server accepts TCP connections, and
// returns a teardown handle. Mirrors `pg_harness.ts` (Phase 1.5) so we do
// not pull in `npm:testcontainers` for one dependency.
//
// Used by the Phase 9b.B reactive_nats_*.test.ts suite. Each test that
// needs NATS starts/stops its own container — the random port + UUID name
// keep parallel runs safe.
//
// Why Docker shell-out: Testcontainers is a Node-targeted library. Inside
// Deno it requires polyfills + a heavy dep tree. A direct `docker run` is
// one shell call and Deno polls TCP via `Deno.connect`. We name the
// container so teardown is explicit (no Ryuk reaper).

import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";

export interface NatsHandle {
  /** Full NATS URL for clients on the host (nats://127.0.0.1:PORT). */
  url: string;
  /** Random host-mapped port forwarded to container's 4222/tcp. */
  port: number;
  /** Docker container id (long form) returned by `docker run -d`. */
  containerId: string;
  /** Docker container name — useful for `docker kill` in teardown. */
  containerName: string;
  /** Kill the NATS container. Safe to call multiple times. */
  stop: () => Promise<void>;
}

async function findFreePort(): Promise<number> {
  const l = Deno.listen({ port: 0 });
  const p = (l.addr as Deno.NetAddr).port;
  l.close();
  await delay(10);
  return p;
}

async function tcpReady(host: string, port: number): Promise<boolean> {
  try {
    const conn = await Deno.connect({ hostname: host, port });
    conn.close();
    return true;
  } catch (_) {
    return false;
  }
}

async function killContainer(name: string): Promise<void> {
  try {
    await new Deno.Command("docker", {
      args: ["kill", name],
      stdout: "null",
      stderr: "null",
    }).output();
  } catch (_) {
    // --rm means the container is already gone if kill fails.
  }
}

/**
 * Start a NATS server in Docker and wait until the TCP port is accepting
 * connections. Polls every 100ms up to 30s. Caller MUST call
 * `handle.stop()` in a `finally`.
 *
 * Image: `nats:2-alpine` — small (~10MB), official, no JetStream config
 * needed (core pub/sub is what Phase 9b.B uses).
 */
export async function startNats(): Promise<NatsHandle> {
  const port = await findFreePort();
  const host = "127.0.0.1";
  const name = `excalibase-natstest-${crypto.randomUUID().slice(0, 8)}`;

  const run = new Deno.Command("docker", {
    args: [
      "run",
      "-d",
      "--rm",
      "--name",
      name,
      "-p",
      `${port}:4222`,
      "nats:2-alpine",
    ],
    stdout: "piped",
    stderr: "piped",
  });
  const { code, stdout, stderr } = await run.output();
  if (code !== 0) {
    throw new Error(`docker run nats failed: ${new TextDecoder().decode(stderr)}`);
  }
  const containerId = new TextDecoder().decode(stdout).trim();

  const deadline = Date.now() + 30_000;
  let ready = false;
  while (Date.now() < deadline) {
    if (await tcpReady(host, port)) { ready = true; break; }
    await delay(100);
  }
  if (!ready) {
    await killContainer(name);
    throw new Error(`nats did not start on :${port} within 30s`);
  }

  return {
    url: `nats://${host}:${port}`,
    port,
    containerId,
    containerName: name,
    stop: () => killContainer(name),
  };
}
