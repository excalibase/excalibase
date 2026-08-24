// Test harness — spawns the Deno runtime server as a subprocess with a
// random port, exposes a small HTTP client tuned to the runtime protocol,
// and tears the subprocess down on .stop(). Each test that needs a runtime
// starts/stops its own instance so port collisions and shared state can't
// affect siblings.

import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";

export interface RuntimeOptions {
  v2Enabled?: boolean;
  allowedHosts?: string;
  /**
   * If set, the server is started with EXCALIBASE_DB_URL pointed at this
   * Postgres URL. Required for Phase 1 ctx.db tests.
   */
  dbUrl?: string;
  /**
   * If set, EXCALIBASE_PROVISIONING_URL is wired so the runtime's metadata
   * capture path posts callbacks back to this URL. Phase 2 metadata tests
   * stand up a mock provisioning server and pass its URL here.
   */
  provisioningUrl?: string;
  /**
   * Phase 9a: override the mutation BEGIN isolation level. One of
   *  - "SERIALIZABLE" (default at runtime if unset)
   *  - "REPEATABLE READ"
   *  - "READ COMMITTED"
   * Sets EXCALIBASE_MUTATION_ISOLATION; the runtime validates at boot.
   */
  mutationIsolation?: string;
  /**
   * Phase 9a: cap on retry attempts when a mutation hits 40001/40P01.
   * Sets EXCALIBASE_MUTATION_RETRY_MAX; runtime default is 5.
   */
  mutationRetryMax?: number;
  /**
   * Phase 9a: base backoff in milliseconds for the retry loop.
   * Sets EXCALIBASE_MUTATION_RETRY_BACKOFF_MS; runtime default is 50.
   */
  mutationRetryBackoffMs?: number;
}

export interface RuntimeHandle {
  port: number;
  secret: string;
  baseUrl: string;
  deploy: (id: string, code: string, secrets?: Record<string, string>) => Promise<Response>;
  invoke: (id: string, body: unknown, headers?: Record<string, string>) => Promise<{
    status: number;
    headers: Record<string, string>;
    body: string;
  }>;
  raw: (path: string, init?: RequestInit) => Promise<Response>;
  stop: () => Promise<void>;
}

const SECRET = "test-runtime-secret";
const SERVER_PATH = new URL("../server.ts", import.meta.url).pathname;

// pickPort picks an ephemeral port. Bind+release is racy but acceptable for
// tests — we retry once on EADDRINUSE inside the spawn loop.
async function pickPort(): Promise<number> {
  const l = Deno.listen({ port: 0 });
  const p = (l.addr as Deno.NetAddr).port;
  l.close();
  await delay(10);
  return p;
}

export async function startRuntime(opts: RuntimeOptions = {}): Promise<RuntimeHandle> {
  const port = await pickPort();
  const env: Record<string, string> = {
    RUNTIME_SECRET: SECRET,
    PORT: String(port),
  };
  if (opts.v2Enabled) env.EXCALIBASE_FUNCTIONS_V2 = "1";
  if (opts.allowedHosts) env.ALLOWED_HOSTS = opts.allowedHosts;
  if (opts.dbUrl) env.EXCALIBASE_DB_URL = opts.dbUrl;
  if (opts.provisioningUrl) env.EXCALIBASE_PROVISIONING_URL = opts.provisioningUrl;
  if (opts.mutationIsolation) env.EXCALIBASE_MUTATION_ISOLATION = opts.mutationIsolation;
  if (opts.mutationRetryMax !== undefined) env.EXCALIBASE_MUTATION_RETRY_MAX = String(opts.mutationRetryMax);
  if (opts.mutationRetryBackoffMs !== undefined) env.EXCALIBASE_MUTATION_RETRY_BACKOFF_MS = String(opts.mutationRetryBackoffMs);

  const cmd = new Deno.Command(Deno.execPath(), {
    args: [
      "run",
      "--allow-net",
      "--allow-env",
      // npm:postgres@3 lives in Deno's npm cache; the main thread needs
      // read access to import it. Workers themselves remain read-disabled
      // (--allow-read here applies to the parent process, not the worker
      // permissions object passed to `new Worker`).
      "--allow-read",
      "--unstable-worker-options",
      SERVER_PATH,
    ],
    env,
    stdout: "piped",
    stderr: "piped",
  });
  const child = cmd.spawn();

  const baseUrl = `http://127.0.0.1:${port}`;
  // Wait for /health to come up, up to 10s.
  const deadline = Date.now() + 10_000;
  let ready = false;
  while (Date.now() < deadline) {
    try {
      const r = await fetch(`${baseUrl}/health`);
      if (r.ok) {
        await r.body?.cancel();
        ready = true;
        break;
      }
      await r.body?.cancel();
    } catch (_) {
      // Server not up yet, keep trying.
    }
    await delay(100);
  }
  if (!ready) {
    try { child.kill("SIGTERM"); } catch (_) { /* ignore */ }
    // Drain stderr to surface the boot error.
    let errText = "";
    try {
      const reader = child.stderr.getReader();
      const { value } = await reader.read();
      errText = value ? new TextDecoder().decode(value) : "";
      reader.releaseLock();
    } catch (_) { /* ignore */ }
    throw new Error(`runtime failed to start on :${port}: ${errText.slice(0, 500)}`);
  }

  // Drain stdout/stderr in the background so the subprocess buffers don't fill.
  // When `HARNESS_VERBOSE=1` we forward the bytes to our own stderr so test
  // authors can see runtime logs while debugging without keeping a custom
  // patched harness around. Off by default to keep CI output tidy.
  const verbose = Deno.env.get("HARNESS_VERBOSE") === "1";
  const drain = async (stream: ReadableStream<Uint8Array>) => {
    const reader = stream.getReader();
    try {
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        if (verbose && value) await Deno.stderr.write(value);
      }
    } catch (_) { /* ignore */ }
  };
  drain(child.stdout);
  drain(child.stderr);

  const headers = (extra?: Record<string, string>): Record<string, string> => ({
    "Content-Type": "application/json",
    "X-Runtime-Secret": SECRET,
    ...(extra ?? {}),
  });

  return {
    port,
    secret: SECRET,
    baseUrl,
    deploy: (id, code, secrets = {}) =>
      fetch(`${baseUrl}/deploy`, {
        method: "POST",
        headers: headers(),
        body: JSON.stringify({ id, code, secrets }),
      }),
    invoke: async (id, body, extraHeaders = {}) => {
      // Retry once on ECONNRESET — the Deno runtime occasionally drops
      // a connection mid-response when spawning a Worker under load. The
      // request is idempotent (POST /invoke with the same body), so a
      // single retry is safe and removes the flake without papering over
      // real failures (which surface on the second attempt unchanged).
      const doFetch = () => fetch(`${baseUrl}/invoke/${id}`, {
        method: "POST",
        headers: headers(),
        body: JSON.stringify({
          method: "POST",
          url: `/invoke/${id}`,
          headers: extraHeaders,
          body: typeof body === "string" ? body : JSON.stringify(body),
        }),
      });
      let res: Response;
      try {
        res = await doFetch();
      } catch (e) {
        const msg = (e instanceof Error ? e.message : String(e)) ?? "";
        if (!/ECONNRESET|connection (reset|closed)/i.test(msg)) throw e;
        await delay(50);
        res = await doFetch();
      }
      const json = await res.json() as {
        status?: number;
        headers?: Record<string, string>;
        body?: string;
        error?: string;
      };
      if (json.error) {
        return { status: res.status, headers: {}, body: json.error };
      }
      return {
        status: json.status ?? 0,
        headers: json.headers ?? {},
        body: json.body ?? "",
      };
    },
    raw: (path, init) => fetch(`${baseUrl}${path}`, init),
    stop: async () => {
      try { child.kill("SIGTERM"); } catch (_) { /* ignore */ }
      // Wait up to 5s for graceful shutdown; if still alive, SIGKILL.
      let exited = false;
      try {
        await Promise.race([
          child.status.then(() => { exited = true; }),
          delay(5_000),
        ]);
      } catch (_) { /* ignore */ }
      if (!exited) {
        try { child.kill("SIGKILL"); } catch (_) { /* ignore */ }
        try { await Promise.race([child.status, delay(2_000)]); } catch (_) { /* ignore */ }
      }
      // Small grace so the OS releases the port (TIME_WAIT) before the next
      // test in the suite calls pickPort() and reuses the same number.
      await delay(100);
    },
  };
}

// makeUnsignedJwt builds a header.payload.signature triplet whose signature
// is bogus — fine for the worker since it only decodes the payload via
// jose.decodeJwt (the Go gateway already verified signature upstream).
export function makeUnsignedJwt(claims: Record<string, unknown>): string {
  const header = { alg: "ES256", typ: "JWT" };
  const enc = (obj: unknown) =>
    btoa(JSON.stringify(obj))
      .replace(/=+$/, "")
      .replace(/\+/g, "-")
      .replace(/\//g, "_");
  return `${enc(header)}.${enc(claims)}.bogus-signature`;
}
