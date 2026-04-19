// Excalibase Deno edge-function runtime — Supabase-compatible shape.
//
// Protocol (platform → runtime):
//   POST /deploy        { id, code, secrets }        — register/replace a function
//   POST /invoke/{id}   InvokeRequest                — run a function, get InvokeResponse
//   DELETE /delete/{id}                              — unregister a function
//   GET /health                                      — liveness
//
// InvokeRequest = { method, url, headers, body }
// InvokeResponse = { status, headers, body }
//
// The worker's user code must export a default handler of shape
//   (req: Request) => Response | Promise<Response>
// The platform's bundler rewrites `export default X` into
// `globalThis.__excalibase_default = X` before sending, so the worker can
// just read it from globalThis after loading the module.
//
// Security: every request (except /health) requires X-Runtime-Secret header.
// Workers run with net restricted to ALLOWED_HOSTS (or disabled) and NO
// filesystem, env, run, ffi, or write permission. Secrets are injected as
// a Deno.env mock so user code sees only its own project's env vars.

interface DeployRequest {
  id: string;
  code: string;
  secrets?: Record<string, string>;
}

interface InvokeRequest {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: string;
}

interface InvokeResponse {
  status: number;
  headers: Record<string, string>;
  body: string;
}

interface PendingRequest {
  resolve: (r: InvokeResponse) => void;
  reject: (e: Error) => void;
  timeout: number;
}

interface LogEntry {
  level: string;
  msg: string;
  ts: number;
}

interface ScriptMetadata {
  id: string;
  worker: Worker;
  createdAt: Date;
  invocations: number;
  // pending tracks in-flight invocations keyed by request id so concurrent
  // calls to the same function don't overwrite each other's response handlers.
  pending: Map<number, PendingRequest>;
  nextReqId: number;
  // Ring buffer of recent user-code log lines. Capped at LOG_RING_SIZE.
  logs: LogEntry[];
}

/**
 * Constant-time string comparison. Avoids early-exit timing leaks when
 * checking the runtime auth secret. Length difference still leaks (but
 * RUNTIME_SECRET length is constant).
 */
function constantTimeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let result = 0;
  for (let i = 0; i < a.length; i++) {
    result |= a.charCodeAt(i) ^ b.charCodeAt(i);
  }
  return result === 0;
}

const RUNTIME_SECRET = Deno.env.get("RUNTIME_SECRET");
if (!RUNTIME_SECRET) {
  console.error("FATAL: RUNTIME_SECRET environment variable is required");
  Deno.exit(1);
}

const MAX_CODE_SIZE = 512 * 1024; // 512 KB
const MAX_INVOKE_BODY = 1024 * 1024; // 1 MB
const MAX_SCRIPTS = 100;
const INVOKE_TIMEOUT_MS = 30_000;
const WORKER_INIT_TIMEOUT_MS = 5_000;
const VALID_ID = /^[a-zA-Z0-9_\-]{1,128}$/;
// Per-function log ring buffer capacity. Old entries are dropped first.
const LOG_RING_SIZE = 100;
// Cap on a single log line so one huge console.log() can't blow up memory.
const MAX_LOG_LINE = 4 * 1024;

// Allowed network hosts for workers — only project Postgres services
// Format: "host1:port1,host2:port2" or empty for no network access
const ALLOWED_HOSTS = (Deno.env.get("ALLOWED_HOSTS") || "").split(",").filter(Boolean);

/** Build the JS source that runs inside the Deno Web Worker. */
function buildWorkerCode(userCode: string, secrets: Record<string, string>): string {
  // `Deno` is frozen inside workers, so we can't reassign `globalThis.Deno`.
  // Instead we wrap user code in an IIFE that shadows `Deno` with a mock via
  // a parameter. Inside the IIFE, any `Deno.env.get(...)` lookup resolves to
  // our mock; outside, the real `Deno` is untouched. Worker permissions still
  // apply either way.
  //
  // We also expose a plain `env` helper (`env.KEY`) for ergonomics — users
  // migrating from Supabase get `Deno.env.get`, new users get `env.KEY`.
  const secretsJSON = JSON.stringify(secrets);
  return `
    // --- console interceptor ---
    // Proxy console.* so user log output is streamed back to the runtime
    // and stored in a per-function ring buffer. Original console.* is still
    // called so logs also land on the pod stdout for operators.
    (function() {
      const LEVELS = ['log', 'info', 'warn', 'error', 'debug'];
      const MAX_LINE = ${MAX_LOG_LINE};
      const fmt = (args) => {
        try {
          return args.map((a) => {
            if (typeof a === 'string') return a;
            if (a instanceof Error) return a.stack || a.message || String(a);
            try { return JSON.stringify(a); } catch (_) { return String(a); }
          }).join(' ');
        } catch (_) { return '[unformattable]'; }
      };
      for (const lvl of LEVELS) {
        const orig = console[lvl] ? console[lvl].bind(console) : console.log.bind(console);
        console[lvl] = function() {
          const args = Array.prototype.slice.call(arguments);
          let msg = fmt(args);
          if (msg.length > MAX_LINE) msg = msg.slice(0, MAX_LINE) + '…';
          try {
            self.postMessage({ type: 'log', level: lvl, msg: msg, ts: Date.now() });
          } catch (_) { /* ignore */ }
          try { orig.apply(null, args); } catch (_) { /* ignore */ }
        };
      }
    })();

    (function(Deno, env) {
      // --- user bundled code (may assign globalThis.__excalibase_default) ---
      ${userCode}
    })(
      // Shadowed Deno — .env is our mock, everything else is copied from the real Deno.
      (() => {
        const __secrets = ${secretsJSON};
        const mockEnv = {
          get(key) { return __secrets[key]; },
          has(key) { return Object.prototype.hasOwnProperty.call(__secrets, key); },
          toObject() { return { ...__secrets }; },
          set() { /* read-only */ },
          delete() { /* read-only */ },
        };
        const shadowed = Object.create(globalThis.Deno);
        Object.defineProperty(shadowed, 'env', { value: mockEnv, enumerable: true });
        Object.defineProperty(shadowed, 'serve', {
          value: () => {
            throw new Error('Deno.serve is not available inside Excalibase functions — export a default handler instead');
          },
          enumerable: true,
        });
        return shadowed;
      })(),
      // Plain env namespace (non-Deno code path)
      (() => {
        const __secrets = ${secretsJSON};
        return {
          get(key) { return __secrets[key]; },
          has(key) { return Object.prototype.hasOwnProperty.call(__secrets, key); },
          toObject() { return { ...__secrets }; },
        };
      })()
    );

    // --- worker dispatch ---
    // Each invoke message carries a unique reqId so the runtime can correlate
    // concurrent responses on the same worker without races.
    self.onmessage = async (e) => {
      const msg = e.data;
      if (msg && msg.type === 'invoke') {
        const reqId = msg.reqId;
        const reqData = msg.data;
        try {
          const init = { method: reqData.method || 'GET', headers: reqData.headers || {} };
          if (reqData.body && reqData.method !== 'GET' && reqData.method !== 'HEAD') {
            init.body = reqData.body;
          }
          // Accept relative URLs from the platform (e.g. /api/.../invoke path).
          // Request() requires an absolute URL, so prepend a synthetic base.
          let url = reqData.url || '/';
          if (!/^https?:\\/\\//.test(url)) {
            url = 'http://fn.excalibase.local' + (url.startsWith('/') ? '' : '/') + url;
          }
          const req = new Request(url, init);

          const handler = globalThis.__excalibase_default;
          if (typeof handler !== 'function') {
            throw new Error('No default export found — function must export default (req: Request) => Response');
          }

          let res = await handler(req);
          // Normalise non-Response returns into JSON responses
          if (!(res instanceof Response)) {
            res = Response.json(res);
          }

          const bodyText = await res.text();
          const headers = {};
          res.headers.forEach((v, k) => { headers[k] = v; });
          self.postMessage({ type: 'success', reqId, status: res.status, headers, body: bodyText });
        } catch (err) {
          self.postMessage({ type: 'error', reqId, error: String(err && err.message || err) });
        }
      }
    };
    self.postMessage({ type: 'ready' });
  `;
}

class FunctionRuntime {
  private scripts = new Map<string, ScriptMetadata>();

  async deploy(req: DeployRequest): Promise<{ id: string; url: string }> {
    const { id, code, secrets = {} } = req;

    if (!id || !VALID_ID.test(id)) {
      throw new Error("Invalid function id");
    }
    if (!code || code.length > MAX_CODE_SIZE) {
      throw new Error(`code exceeds maximum size (${MAX_CODE_SIZE / 1024} KB)`);
    }
    if (this.scripts.size >= MAX_SCRIPTS && !this.scripts.has(id)) {
      throw new Error(`max scripts limit reached (${MAX_SCRIPTS})`);
    }

    // Replace any existing worker
    const existing = this.scripts.get(id);
    if (existing) existing.worker.terminate();

    const workerCode = buildWorkerCode(code, secrets);
    // TypeScript MIME type tells Deno to treat the blob as TS and strip type
    // annotations. Without this, user code with `req: Request` annotations
    // fails to parse as plain JS.
    const blob = new Blob([workerCode], { type: "application/typescript" });

    const netPermission: boolean | string[] =
      ALLOWED_HOSTS.length > 0 ? ALLOWED_HOSTS : false;

    const worker = new Worker(URL.createObjectURL(blob), {
      type: "module",
      // deno-lint-ignore no-explicit-any
      deno: {
        permissions: {
          net: netPermission,
          env: false,
          read: false,
          write: false,
          run: false,
          ffi: false,
        },
      },
    } as any);

    // Wait for the worker's 'ready' message. Installs a temporary handler
    // that swaps to the permanent router on first 'ready'.
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => {
        // Terminate the orphan so we don't leak it on init failure.
        try { worker.terminate(); } catch (_) { /* ignore */ }
        reject(new Error("worker init timeout"));
      }, WORKER_INIT_TIMEOUT_MS);
      worker.onmessage = (e) => {
        if (e.data?.type === "ready") {
          clearTimeout(timeout);
          resolve();
        }
      };
      worker.onerror = (err) => {
        clearTimeout(timeout);
        try { worker.terminate(); } catch (_) { /* ignore */ }
        reject(err);
      };
    });

    const meta: ScriptMetadata = {
      id,
      worker,
      createdAt: new Date(),
      invocations: 0,
      pending: new Map(),
      nextReqId: 1,
      logs: [],
    };
    this.scripts.set(id, meta);

    // Permanent message router — dispatches responses to pending requests by reqId
    // and appends log messages to the ring buffer. Installed AFTER init handshake
    // so the 'ready' message above lands on the temporary handler.
    worker.onmessage = (e) => {
      const msg = e.data;
      if (!msg) return;

      if (msg.type === "log") {
        const level = typeof msg.level === "string" ? msg.level : "log";
        const text = typeof msg.msg === "string" ? msg.msg : "";
        const ts = typeof msg.ts === "number" ? msg.ts : Date.now();
        meta.logs.push({ level, msg: text, ts });
        if (meta.logs.length > LOG_RING_SIZE) {
          meta.logs.splice(0, meta.logs.length - LOG_RING_SIZE);
        }
        return;
      }

      if (typeof msg.reqId !== "number") return;
      const pending = meta.pending.get(msg.reqId);
      if (!pending) return; // late delivery after timeout — ignore
      meta.pending.delete(msg.reqId);
      clearTimeout(pending.timeout);
      metrics.invocationsTotal++;
      if (msg.type === "success") {
        pending.resolve({
          status: msg.status || 200,
          headers: msg.headers || {},
          body: msg.body || "",
        });
      } else {
        metrics.invocationsError++;
        pending.reject(new Error(msg.error || "worker error"));
      }
    };

    // Permanent error handler — fail every in-flight request and tear down
    // the dead worker so the next deploy can replace it cleanly.
    worker.onerror = (err) => {
      console.error(`[runtime] worker ${id} crashed: ${err.message || "unknown"}`);
      for (const [reqId, p] of meta.pending) {
        clearTimeout(p.timeout);
        p.reject(new Error(`worker crashed: ${err.message || "unknown"}`));
        meta.pending.delete(reqId);
      }
      try { worker.terminate(); } catch (_) { /* ignore */ }
      this.scripts.delete(id);
    };

    metrics.deploysTotal++;
    console.log(`[runtime] deployed ${id} (${Object.keys(secrets).length} secrets)`);
    return { id, url: `/invoke/${id}` };
  }

  async invoke(id: string, req: InvokeRequest): Promise<InvokeResponse> {
    const script = this.scripts.get(id);
    if (!script) throw new Error(`function not found: ${id}`);

    script.invocations++;
    const reqId = script.nextReqId++;

    return await new Promise<InvokeResponse>((resolve, reject) => {
      const timeout = setTimeout(() => {
        script.pending.delete(reqId);
        metrics.invocationsTotal++;
        metrics.invocationsError++;
        metrics.timeoutsTotal++;
        reject(new Error(`execution timeout (${INVOKE_TIMEOUT_MS / 1000}s)`));

        // If this was the last pending request and nothing succeeded since
        // the timeout fired, the worker is likely stuck (infinite loop, deadlock).
        // Terminate it to reclaim resources. The function can be redeployed
        // on the next deploy call.
        if (script.pending.size === 0) {
          console.error(`[runtime] terminating stuck worker ${id} (no pending requests after timeout)`);
          try { script.worker.terminate(); } catch (_) { /* ignore */ }
          this.scripts.delete(id);
        }
      }, INVOKE_TIMEOUT_MS);
      script.pending.set(reqId, { resolve, reject, timeout });
      script.worker.postMessage({ type: "invoke", reqId, data: req });
    });
  }

  delete(id: string): boolean {
    const script = this.scripts.get(id);
    if (!script) return false;
    script.worker.terminate();
    this.scripts.delete(id);
    return true;
  }

  list() {
    return Array.from(this.scripts.values()).map((s) => ({
      id: s.id,
      invocations: s.invocations,
      uptime: Date.now() - s.createdAt.getTime(),
      pending: s.pending.size,
    }));
  }

  // getLogs returns the ring buffer for a function, optionally filtered to
  // entries strictly newer than the given timestamp (milliseconds). If the
  // function doesn't exist, returns null so the caller can return 404.
  getLogs(id: string, sinceMs?: number): LogEntry[] | null {
    const script = this.scripts.get(id);
    if (!script) return null;
    if (typeof sinceMs === "number" && Number.isFinite(sinceMs)) {
      return script.logs.filter((l) => l.ts > sinceMs);
    }
    return script.logs.slice();
  }

  stats() {
    return {
      totalScripts: this.scripts.size,
      maxScripts: MAX_SCRIPTS,
      scripts: this.list(),
    };
  }
}

// --- Metrics (Prometheus text format) ---
// Simple counters tracked globally. No histogram (adds complexity for minimal value
// at this scale). Operators who need percentiles should use the /logs endpoint or
// instrument upstream in the Go handler.
const metrics = {
  invocationsTotal: 0,
  invocationsError: 0,
  deploysTotal: 0,
  timeoutsTotal: 0,
};

const runtime = new FunctionRuntime();

Deno.serve({ port: 8000 }, async (req: Request) => {
  const url = new URL(req.url);
  const headers: Record<string, string> = { "Content-Type": "application/json" };

  if (req.method === "OPTIONS") {
    return new Response(null, { status: 204, headers });
  }

  // Authenticate everything except /health (constant-time compare to avoid
  // timing oracle on the runtime secret).
  if (url.pathname !== "/health") {
    const provided = req.headers.get("X-Runtime-Secret") ?? "";
    if (!constantTimeEqual(provided, RUNTIME_SECRET!)) {
      return Response.json({ error: "forbidden" }, { status: 403, headers });
    }
  }

  // Refuse oversized requests upfront via Content-Length, before reading the
  // body into memory. Stops trivial DOS via 1 GB POST.
  const checkContentLength = (max: number): Response | null => {
    const cl = req.headers.get("content-length");
    if (cl) {
      const n = Number(cl);
      if (Number.isFinite(n) && n > max) {
        return Response.json({ error: "payload too large" }, { status: 413, headers });
      }
    }
    return null;
  };

  try {
    if (url.pathname === "/health") {
      return Response.json(
        { status: "healthy", scripts: runtime.stats().totalScripts, uptime: performance.now() },
        { headers },
      );
    }

    if (url.pathname === "/deploy" && req.method === "POST") {
      const deployMax = MAX_CODE_SIZE + 16 * 1024;
      const tooBig = checkContentLength(deployMax);
      if (tooBig) return tooBig;
      const body = await req.text();
      if (body.length > deployMax) {
        return Response.json({ error: "payload too large" }, { status: 413, headers });
      }
      const parsed = JSON.parse(body) as DeployRequest;
      if (!parsed.id || !parsed.code) {
        return Response.json({ error: "missing id or code" }, { status: 400, headers });
      }
      const result = await runtime.deploy(parsed);
      return Response.json(result, { status: 201, headers });
    }

    if (url.pathname.startsWith("/invoke/") && req.method === "POST") {
      const id = decodeURIComponent(url.pathname.slice("/invoke/".length));
      if (!VALID_ID.test(id)) {
        return Response.json({ error: "invalid function id" }, { status: 400, headers });
      }
      const tooBig = checkContentLength(MAX_INVOKE_BODY);
      if (tooBig) return tooBig;
      const body = await req.text();
      if (body.length > MAX_INVOKE_BODY) {
        return Response.json({ error: "payload too large" }, { status: 413, headers });
      }
      const invokeReq = JSON.parse(body) as InvokeRequest;
      const result = await runtime.invoke(id, invokeReq);
      return Response.json(result, { headers });
    }

    if (url.pathname.startsWith("/logs/") && req.method === "GET") {
      const id = decodeURIComponent(url.pathname.slice("/logs/".length));
      if (!VALID_ID.test(id)) {
        return Response.json({ error: "invalid function id" }, { status: 400, headers });
      }
      const sinceRaw = url.searchParams.get("since");
      const sinceMs = sinceRaw ? Number(sinceRaw) : undefined;
      const logs = runtime.getLogs(id, sinceMs);
      if (logs === null) {
        return Response.json({ error: "not found" }, { status: 404, headers });
      }
      return Response.json({ logs }, { headers });
    }

    if (url.pathname.startsWith("/delete/") && req.method === "DELETE") {
      const id = decodeURIComponent(url.pathname.slice("/delete/".length));
      if (!VALID_ID.test(id)) {
        return Response.json({ error: "invalid function id" }, { status: 400, headers });
      }
      return runtime.delete(id)
        ? Response.json({ status: "deleted", id }, { headers })
        : Response.json({ error: "not found" }, { status: 404, headers });
    }

    if (url.pathname === "/scripts") {
      return Response.json({ scripts: runtime.list() }, { headers });
    }

    if (url.pathname === "/stats") {
      return Response.json(runtime.stats(), { headers });
    }

    if (url.pathname === "/metrics") {
      const stats = runtime.stats();
      const lines = [
        "# HELP excalibase_fn_scripts_active Number of deployed function workers",
        "# TYPE excalibase_fn_scripts_active gauge",
        `excalibase_fn_scripts_active ${stats.totalScripts}`,
        "",
        "# HELP excalibase_fn_scripts_max Maximum number of function workers",
        "# TYPE excalibase_fn_scripts_max gauge",
        `excalibase_fn_scripts_max ${stats.maxScripts}`,
        "",
        "# HELP excalibase_fn_invocations_total Total function invocations",
        "# TYPE excalibase_fn_invocations_total counter",
        `excalibase_fn_invocations_total ${metrics.invocationsTotal}`,
        "",
        "# HELP excalibase_fn_invocations_errors_total Total failed invocations",
        "# TYPE excalibase_fn_invocations_errors_total counter",
        `excalibase_fn_invocations_errors_total ${metrics.invocationsError}`,
        "",
        "# HELP excalibase_fn_deploys_total Total function deployments",
        "# TYPE excalibase_fn_deploys_total counter",
        `excalibase_fn_deploys_total ${metrics.deploysTotal}`,
        "",
        "# HELP excalibase_fn_timeouts_total Total invocation timeouts",
        "# TYPE excalibase_fn_timeouts_total counter",
        `excalibase_fn_timeouts_total ${metrics.timeoutsTotal}`,
        "",
        "# HELP excalibase_fn_uptime_seconds Runtime uptime in seconds",
        "# TYPE excalibase_fn_uptime_seconds gauge",
        `excalibase_fn_uptime_seconds ${Math.floor(performance.now() / 1000)}`,
        "",
      ];
      return new Response(lines.join("\n"), {
        status: 200,
        headers: { "Content-Type": "text/plain; version=0.0.4; charset=utf-8" },
      });
    }

    return Response.json({ error: "not found" }, { status: 404, headers });
  } catch (error: any) {
    // Never leak stack traces
    const msg = String(error?.message || "internal error").slice(0, 500);
    return Response.json({ error: msg }, { status: 500, headers });
  }
});

console.log("Excalibase Deno runtime on :8000");
