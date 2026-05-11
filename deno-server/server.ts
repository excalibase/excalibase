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
    // codePointAt is preferred over charCodeAt; for ASCII secrets the values
    // are identical, but the wider type satisfies modern lint rules.
    const ca = a.codePointAt(i) ?? 0;
    const cb = b.codePointAt(i) ?? 0;
    result |= ca ^ cb;
  }
  return result === 0;
}

const RUNTIME_SECRET_RAW = Deno.env.get("RUNTIME_SECRET");
if (!RUNTIME_SECRET_RAW) {
  console.error("FATAL: RUNTIME_SECRET environment variable is required");
  Deno.exit(1);
}
const RUNTIME_SECRET: string = RUNTIME_SECRET_RAW;

const MAX_CODE_SIZE = 512 * 1024; // 512 KB
const MAX_INVOKE_BODY = 1024 * 1024; // 1 MB
const MAX_SCRIPTS = 100;
const INVOKE_TIMEOUT_MS = 30_000;
const WORKER_INIT_TIMEOUT_MS = 5_000;
const VALID_ID = /^[a-zA-Z0-9_-]{1,128}$/;
// Per-function log ring buffer capacity. Old entries are dropped first.
const LOG_RING_SIZE = 100;
// Cap on a single log line so one huge console.log() can't blow up memory.
const MAX_LOG_LINE = 4 * 1024;

// Allowed network hosts for workers — only project Postgres services
// Format: "host1:port1,host2:port2" or empty for no network access
const ALLOWED_HOSTS = (Deno.env.get("ALLOWED_HOSTS") || "").split(",").filter(Boolean);

// Server port — defaults to 8000 for production; tests override via env so
// concurrent test runs don't collide on the same port.
const PORT = Number(Deno.env.get("PORT") || "8000");

// Feature flag for the v2 tagged FunctionDef shape (kind: "query" |
// "mutation" | "action"). When off (default), v2-shaped exports fall back
// through to the legacy Fetch handler path, which will fail naturally
// because the export is an object rather than a function. When on, the
// worker recognises the shape, builds a ctx skeleton (auth.claims from the
// incoming Bearer JWT, db: null in this phase), and dispatches the
// handler. Existing legacy Fetch handlers are unaffected either way.
const V2_ENABLED = Deno.env.get("EXCALIBASE_FUNCTIONS_V2") === "1" ||
  Deno.env.get("EXCALIBASE_FUNCTIONS_V2") === "true";

// V2_KINDS — recognised tagged FunctionDef kinds. Used both in the worker
// (kept as a literal in the template) and in shape checks here. Phase 3
// codegen will key off these too.
const V2_KINDS = ["query", "mutation", "action"];

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
  return String.raw`
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
          try { orig.apply(null, args); } catch { /* suppress original console errors */ }
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

    // --- v2 helpers (only used when EXCALIBASE_FUNCTIONS_V2 is on) ---
    const __V2_ENABLED = ${V2_ENABLED ? "true" : "false"};
    const __V2_KINDS = ${JSON.stringify(V2_KINDS)};

    // __isV2Export — duck-types a tagged FunctionDef record:
    //   { kind: "query"|"mutation"|"action", args: <zod-or-parseable>, handler: function }
    // We accept any object whose 'args' is non-null because the user's zod
    // (or hand-rolled) parser will run inside the handler shim below.
    function __isV2Export(d) {
      if (!d || typeof d !== 'object') return false;
      if (typeof d.handler !== 'function') return false;
      if (d.args === null || d.args === undefined) return false;
      return __V2_KINDS.indexOf(d.kind) !== -1;
    }

    // __decodeJwtClaims — base64url-decode the JWT payload segment. The Go
    // gateway has already verified the signature before forwarding; this
    // worker only reads claims for ctx.auth. Returns null on a malformed
    // or absent token so the handler can still run unauthenticated.
    function __decodeJwtClaims(authHeader) {
      if (typeof authHeader !== 'string') return null;
      if (!authHeader.toLowerCase().startsWith('bearer ')) return null;
      const token = authHeader.slice(7).trim();
      if (!token) return null;
      const parts = token.split('.');
      if (parts.length !== 3) return null;
      try {
        // base64url → base64 (atob is base64 only)
        let p = parts[1].replace(/-/g, '+').replace(/_/g, '/');
        const pad = p.length % 4;
        if (pad === 2) p += '==';
        else if (pad === 3) p += '=';
        else if (pad !== 0) return null;
        const json = atob(p);
        return JSON.parse(json);
      } catch (_) {
        return null;
      }
    }

    // __dispatchV2 — runs the tagged FunctionDef contract:
    //   POST body must be { "args": <object> }; on missing args, 400.
    //   Calls handler(ctx, body.args); wraps result as { data } JSON.
    //   ctx.db is null in phase 0 — phase 1 will replace it with a real
    //   transaction-scoped client.
    async function __dispatchV2(reqId, reqData, fnDef) {
      try {
        const headers = reqData.headers || {};
        const auth = headers['Authorization'] || headers['authorization'] || '';
        const claims = __decodeJwtClaims(auth);

        let body = {};
        if (reqData.body) {
          try { body = JSON.parse(reqData.body); } catch (_) {
            self.postMessage({ type: 'success', reqId, status: 400,
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ error: 'invalid JSON body' }) });
            return;
          }
        }
        if (!body || typeof body !== 'object' || !('args' in body)) {
          self.postMessage({ type: 'success', reqId, status: 400,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ error: "request body must contain 'args' field" }) });
          return;
        }

        const ctx = { db: null, auth: { claims } };
        try {
          const result = await fnDef.handler(ctx, body.args);
          self.postMessage({ type: 'success', reqId, status: 200,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ data: result === undefined ? null : result }) });
        } catch (err) {
          // If the handler threw a zod-style issues array, relay it as 400.
          if (err && Array.isArray(err.issues)) {
            self.postMessage({ type: 'success', reqId, status: 400,
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ error: 'args validation failed', issues: err.issues }) });
            return;
          }
          self.postMessage({ type: 'success', reqId, status: 500,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ error: String(err && err.message || err) }) });
        }
      } catch (outer) {
        self.postMessage({ type: 'error', reqId, error: String(outer && outer.message || outer) });
      }
    }

    // --- worker dispatch ---
    // Each invoke message carries a unique reqId so the runtime can correlate
    // concurrent responses on the same worker without races.
    self.onmessage = async (e) => {
      const msg = e.data;
      if (msg && msg.type === 'invoke') {
        const reqId = msg.reqId;
        const reqData = msg.data;

        // v2 path — only if the flag is on AND the export matches the
        // tagged FunctionDef shape. Anything else falls through to legacy.
        const exp = globalThis.__excalibase_default;
        if (__V2_ENABLED && __isV2Export(exp)) {
          await __dispatchV2(reqId, reqData, exp);
          return;
        }

        try {
          const init = { method: reqData.method || 'GET', headers: reqData.headers || {} };
          if (reqData.body && reqData.method !== 'GET' && reqData.method !== 'HEAD') {
            init.body = reqData.body;
          }
          // Accept relative URLs from the platform (e.g. /api/.../invoke path).
          // Request() requires an absolute URL, so prepend a synthetic base.
          let url = reqData.url || '/';
          if (!/^https?:\/\//.test(url)) {
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
  private readonly scripts = new Map<string, ScriptMetadata>();

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
        try { worker.terminate(); } catch (terminateErr) { console.debug("[runtime] terminate on timeout:", terminateErr); }
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
        try { worker.terminate(); } catch (terminateErr) { console.debug("[runtime] terminate on error:", terminateErr); }
        reject(new Error(err.message ?? "worker init error"));
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
      try { worker.terminate(); } catch (terminateErr) { console.debug("[runtime] terminate on crash:", terminateErr); }
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
          try { script.worker.terminate(); } catch (terminateErr) { console.debug("[runtime] terminate on invoke timeout:", terminateErr); }
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

const JSON_HEADERS: Record<string, string> = { "Content-Type": "application/json" };

// checkContentLength refuses oversized requests upfront via Content-Length,
// before the body is read into memory. Returns a 413 Response on overflow.
function checkContentLength(req: Request, max: number): Response | null {
  const cl = req.headers.get("content-length");
  if (cl) {
    const n = Number(cl);
    if (Number.isFinite(n) && n > max) {
      return Response.json({ error: "payload too large" }, { status: 413, headers: JSON_HEADERS });
    }
  }
  return null;
}

function notFound(): Response {
  return Response.json({ error: "not found" }, { status: 404, headers: JSON_HEADERS });
}

function badRequest(msg: string): Response {
  return Response.json({ error: msg }, { status: 400, headers: JSON_HEADERS });
}

async function handleHealth(): Promise<Response> {
  return Response.json(
    { status: "healthy", scripts: runtime.stats().totalScripts, uptime: performance.now() },
    { headers: JSON_HEADERS },
  );
}

async function handleDeploy(req: Request): Promise<Response> {
  const deployMax = MAX_CODE_SIZE + 16 * 1024;
  const tooBig = checkContentLength(req, deployMax);
  if (tooBig) return tooBig;
  const body = await req.text();
  if (body.length > deployMax) {
    return Response.json({ error: "payload too large" }, { status: 413, headers: JSON_HEADERS });
  }
  const parsed = JSON.parse(body) as DeployRequest;
  if (!parsed.id || !parsed.code) {
    return badRequest("missing id or code");
  }
  const result = await runtime.deploy(parsed);
  return Response.json(result, { status: 201, headers: JSON_HEADERS });
}

async function handleInvoke(req: Request, id: string): Promise<Response> {
  if (!VALID_ID.test(id)) return badRequest("invalid function id");
  const tooBig = checkContentLength(req, MAX_INVOKE_BODY);
  if (tooBig) return tooBig;
  const body = await req.text();
  if (body.length > MAX_INVOKE_BODY) {
    return Response.json({ error: "payload too large" }, { status: 413, headers: JSON_HEADERS });
  }
  const invokeReq = JSON.parse(body) as InvokeRequest;
  const result = await runtime.invoke(id, invokeReq);
  return Response.json(result, { headers: JSON_HEADERS });
}

function handleLogs(url: URL, id: string): Response {
  if (!VALID_ID.test(id)) return badRequest("invalid function id");
  const sinceRaw = url.searchParams.get("since");
  const sinceMs = sinceRaw ? Number(sinceRaw) : undefined;
  const logs = runtime.getLogs(id, sinceMs);
  if (logs === null) return notFound();
  return Response.json({ logs }, { headers: JSON_HEADERS });
}

function handleDelete(id: string): Response {
  if (!VALID_ID.test(id)) return badRequest("invalid function id");
  return runtime.delete(id)
    ? Response.json({ status: "deleted", id }, { headers: JSON_HEADERS })
    : notFound();
}

function handleMetrics(): Response {
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

function isAuthorized(req: Request, url: URL): boolean {
  if (url.pathname === "/health") return true;
  const provided = req.headers.get("X-Runtime-Secret") ?? "";
  return constantTimeEqual(provided, RUNTIME_SECRET);
}

// dispatch matches a request to the right route handler. Each handler is a
// thin wrapper so the dispatcher itself stays under Sonar's complexity limit.
async function dispatch(req: Request, url: URL): Promise<Response> {
  if (url.pathname === "/health") return handleHealth();
  if (url.pathname === "/deploy" && req.method === "POST") return handleDeploy(req);
  if (url.pathname.startsWith("/invoke/") && req.method === "POST") {
    return handleInvoke(req, decodeURIComponent(url.pathname.slice("/invoke/".length)));
  }
  if (url.pathname.startsWith("/logs/") && req.method === "GET") {
    return handleLogs(url, decodeURIComponent(url.pathname.slice("/logs/".length)));
  }
  if (url.pathname.startsWith("/delete/") && req.method === "DELETE") {
    return handleDelete(decodeURIComponent(url.pathname.slice("/delete/".length)));
  }
  if (url.pathname === "/scripts") return Response.json({ scripts: runtime.list() }, { headers: JSON_HEADERS });
  if (url.pathname === "/stats") return Response.json(runtime.stats(), { headers: JSON_HEADERS });
  if (url.pathname === "/metrics") return handleMetrics();
  return notFound();
}

Deno.serve({ port: PORT }, async (req: Request) => {
  const url = new URL(req.url);
  if (req.method === "OPTIONS") {
    return new Response(null, { status: 204, headers: JSON_HEADERS });
  }
  if (!isAuthorized(req, url)) {
    return Response.json({ error: "forbidden" }, { status: 403, headers: JSON_HEADERS });
  }
  try {
    return await dispatch(req, url);
  } catch (error: unknown) {
    // Never leak stack traces
    const msg = String((error instanceof Error ? error.message : null) ?? "internal error").slice(0, 500);
    return Response.json({ error: msg }, { status: 500, headers: JSON_HEADERS });
  }
});

console.log(`Excalibase Deno runtime on :${PORT}${V2_ENABLED ? " (functions v2 enabled)" : ""}`);
