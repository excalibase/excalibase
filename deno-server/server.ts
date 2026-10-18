// Excalibase Deno edge-function runtime — Supabase-compatible shape.
//
// Protocol (platform → runtime):
//   POST /deploy        { id, code, secrets }        — register/replace a function
//   POST /invoke/{id}   InvokeRequest                — run a function, get InvokeResponse
//   DELETE /delete/{id}                              — unregister a function
//   GET /health                                      — liveness; { status, scripts, uptime, bootId }
//
// Deployed functions are held in memory only. `bootId` on /health is a random
// id per process; provisioning replays the project's functions from its store
// when it sees the id change (EXC-337).
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
// Workers run with net restricted to the egress allowlist (ALLOWED_HOSTS env
// unioned with the deploy's allowedHosts, or disabled) and NO
// filesystem, env, run, ffi, or write permission. Secrets are injected as
// a Deno.env mock so user code sees only its own project's env vars.
//
// ctx.db was removed with the document data plane (@excalibase/server 0.11.0): every ctx
// carries a ctx.db that refuses use, and functions reach their project's
// data through its GraphQL or REST API (EXC-625).
//
// Local @excalibase/server resolution: the worker template doesn't import
// @excalibase/server directly — Phase 1 contract types are defined in the
// runtime/* modules. The package lives at ../excalibase-server in the
// workspace; once the worker bundler is added (Phase 2/3) it will resolve
// via a `file:` spec rather than the npm registry.

import { newId } from "./runtime/ids.ts";
import { MutationScopes } from "./runtime/mutation_scopes.ts";
import { drainThenExit, shutdownDrainMs } from "./runtime/shutdown.ts";
import { runXFailure, runXTargetError, withoutCrashDetail } from "./runtime/invoke_errors.ts";
import type { InvokeResponse, RunXFailure } from "./runtime/invoke_errors.ts";
import { outboundDeniedMessage } from "./runtime/outbound_denied.ts";

// ---------------------------------------------------------------------------
// Mutation scopes (runtime/mutation_scopes.ts).
//
// A mutation's ctx.scheduler calls are sent to provisioning only once its
// handler succeeds. Each invocation's scope ref rides on the invoke envelope
// and the worker echoes it on every scheduler/runX RPC; a nested
// ctx.runMutation's calls join its caller's only when it succeeds. Actions
// open no scope, so their schedules are sent at once.
// ---------------------------------------------------------------------------

const mutationScopes = new MutationScopes<SchedulerOp>();

interface DeployRequest {
  id: string;
  code: string;
  secrets?: Record<string, string>;
  // EXC-348 — per-project outbound allowlist, in Deno net-permission form
  // (host, host:port, *.suffix[:port] or IP literal). Unioned with the
  // runtime-wide ALLOWED_HOSTS env for this worker only; the control plane
  // validates entries before sending them and the runtime re-checks shape.
  allowedHosts?: string[];
}

interface InvokeRequest {
  method: string;
  url: string;
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
  // Phase 8 — recorded from the metadata callback. The main thread uses
  // this for runX read-only enforcement (query handlers can't call
  // mutations/actions) and for scope routing (a mutation→mutation call
  // joins the caller's mutation scope). One of `"query"|"mutation"|"action"
  // |"httpAction"|"httpRouter"|""` (empty for v1 fetch handlers).
  kind: string;
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
// Allow dot so the Go side's runtime id (`projectId__moduleName.exportName`)
// can register the parity-sweep `db.insert` / `db.find` bundle ids.
// Provisioning's `f.RuntimeID()` is `${projectID}__${f.ID}` and `f.ID` is
// user-supplied (e.g. `db.insert`); without the dot the runtime rejects every
// dotted-id deploy as "Invalid function id".
const VALID_ID = /^[a-zA-Z0-9_.\-]{1,128}$/;
// Per-function log ring buffer capacity. Old entries are dropped first.
const LOG_RING_SIZE = 100;
// Cap on a single log line so one huge console.log() can't blow up memory.
const MAX_LOG_LINE = 4 * 1024;

// Runtime-wide allowed network hosts for workers, in Deno net-permission form:
// "host1:port1,host2,*.suffix" or empty for no network access. On k8s this is
// the project's rendered allowlist (one pod per project); on the shared docker
// runtime it is an operator baseline that each deploy's `allowedHosts` extends.
const ALLOWED_HOSTS = (Deno.env.get("ALLOWED_HOSTS") || "").split(",").filter(Boolean);

// Bounds on a deploy's allowedHosts (EXC-348). Mirrors the control plane's
// MaxEgressHosts; the shape check here only guards against a payload Deno
// would read more loosely than intended (a bare "*", a scheme, a list).
const MAX_ALLOWED_HOSTS = 64;
const MAX_ALLOWED_HOST_LEN = 260;
const ALLOWED_HOST_SHAPE = /^(\*\.)?[a-z0-9.\-\[\]:]+$/i;

// AllowedHostsError marks a deploy refused for its allowedHosts — a client
// error (400), unlike the runtime failures the outer handler maps to 500.
class AllowedHostsError extends Error {}

// parseAllowedHosts validates a deploy payload's allowedHosts. Throws on any
// entry the worker permission must not receive; returns [] when absent.
function parseAllowedHosts(raw: unknown): string[] {
  if (raw === undefined || raw === null) return [];
  if (!Array.isArray(raw) || raw.length > MAX_ALLOWED_HOSTS) {
    throw new AllowedHostsError(`allowedHosts must be an array of at most ${MAX_ALLOWED_HOSTS} hosts`);
  }
  const out: string[] = [];
  for (const entry of raw) {
    if (!isAllowedHostEntry(entry)) {
      throw new AllowedHostsError("allowedHosts entries must be host, host:port or *.suffix");
    }
    if (!out.includes(entry)) out.push(entry);
  }
  return out;
}

function isAllowedHostEntry(entry: unknown): entry is string {
  return typeof entry === "string" && entry.length > 0 && entry.length <= MAX_ALLOWED_HOST_LEN &&
    ALLOWED_HOST_SHAPE.test(entry) && entry !== "*." && !entry.includes("*.*");
}

// egressAllowlist is the runtime-wide list unioned with the deploy's own
// (EXC-348). It becomes the worker's `net` permission; an empty list means
// the worker gets no network access at all.
function egressAllowlist(deployHosts: string[]): boolean | string[] {
  const union = [...ALLOWED_HOSTS];
  for (const host of deployHosts) if (!union.includes(host)) union.push(host);
  return union.length > 0 ? union : false;
}

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

// Phase 2 metadata capture — when set, the runtime POSTs every worker's
// scanned v2 export metadata to ${PROVISIONING_URL}/internal/runtime/
// functions/{fnId}/metadata. Authenticated with the same RUNTIME_SECRET
// the Go side uses to talk to /deploy. Best-effort; failures are logged.
const PROVISIONING_URL = Deno.env.get("EXCALIBASE_PROVISIONING_URL") || "";

// V2_KINDS — recognised tagged FunctionDef kinds. Used both in the worker
// (kept as a literal in the template) and in shape checks here. Phase 3
// codegen will key off these too. Phase 7 extends the family with
// httpAction and httpRouter; these share the v2 boot path but skip the
// {args}-parsing dispatch and go through a raw-Request handler instead.
const V2_KINDS = ["query", "mutation", "action", "httpAction", "httpRouter"];

// EXCALIBASE_RUN_MAX_DEPTH bounds the nested ctx.runQuery/runMutation/
// runAction call chain so a buggy handler can't recurse without limit.
// Default 8 (matches @excalibase/server@0.6.0 CHANGELOG). Each runX RPC
// carries the caller's depth + 1 in its envelope; the dispatcher rejects
// any value above the limit before the message reaches the target worker.
const RUN_MAX_DEPTH = Math.max(1, Number(Deno.env.get("EXCALIBASE_RUN_MAX_DEPTH") || "8"));

// Phase 9b.G — vendored @excalibase/server library resolution.
//
// User bundles emitted by the Go bundler still carry literal
// `import { mutation } from "npm:@excalibase/server@X.Y.Z"` statements;
// the package is workspace-internal and never published to npm. Instead
// the runtime image vendors the lib at /opt/excalibase-server/dist/index.mjs
// and `deno.json` maps every supported version key onto that file.
//
// The worker permissions object below grants scoped `read` access ONLY to
// the vendored directory so the worker can load the file but cannot stat
// any other host path. Inside the container this resolves to
// /app/vendor/excalibase-server (Dockerfile COPY), or /opt/excalibase-server
// when the operator chose the legacy mount layout — we accept both via
// EXCALIBASE_VENDORED_LIB_DIR with the default matching the Dockerfile.
//
// On the host (deno test runs), the harness sets EXCALIBASE_VENDORED_LIB_DIR
// to the symlinked sibling dist; absence of the var falls back to the
// in-tree `./vendor/excalibase-server` relative to cwd.
const VENDORED_LIB_DIR_RAW = Deno.env.get("EXCALIBASE_VENDORED_LIB_DIR") || "";
function resolveVendoredLibDir(): string {
  if (VENDORED_LIB_DIR_RAW) {
    try { return Deno.realPathSync(VENDORED_LIB_DIR_RAW); } catch (_) { return VENDORED_LIB_DIR_RAW; }
  }
  // Default: ./vendor/excalibase-server relative to cwd (matches the
  // Dockerfile's `COPY --from=lib-build /lib/dist ./vendor/excalibase-server`
  // and the host-side symlink the deno-server test harness installs).
  try { return Deno.realPathSync("./vendor/excalibase-server"); } catch (_) { return "./vendor/excalibase-server"; }
}
const VENDORED_LIB_DIR: string = resolveVendoredLibDir();

/** Build the JS source that runs inside the Deno Web Worker. */
function buildWorkerCode(userCode: string, secrets: Record<string, string>): string {
  // Phase 9b.F — user code is loaded as an ESM module via a Blob URL and
  // `await import()`, not wrapped in an IIFE. The old IIFE approach was
  // syntactically incompatible with `import`/`export` statements at the
  // module top level; the Go bundler now emits ESM output for `npm:`
  // imports to resolve in Deno (IIFE's synthesised `__require()` stub
  // throws at worker boot).
  //
  // Secret injection: Deno's `Deno.env` and `Deno.serve` properties are
  // configurable in workers (verified with Object.defineProperty at boot).
  // We replace both on the worker's `globalThis.Deno` BEFORE awaiting the
  // user-module import, so `Deno.env.get(KEY)` inside user code reads
  // from the deploy-time secrets map. A plain `globalThis.env` namespace
  // mirrors the surface for code that prefers `env.KEY`.
  //
  // Backwards compatibility: tests and legacy bundles that assign
  // `globalThis.__excalibase_default = <expr>` as a plain statement still
  // work — the dynamic import evaluates the module body for side effects
  // and the global is populated. We then read `userModule.default` first
  // (the ESM contract) and fall back to the global slot if the module
  // exports nothing.
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

    // --- secret + Deno surface install ---
    // Performed before the user module imports so any module-level
    // Deno.env.get(...) reads the patched env. We do NOT swap globalThis.Deno
    // wholesale (other Deno APIs the user might rely on stay reachable);
    // instead we redefine the two properties that need to differ from the
    // host environment.
    (function() {
      const __secrets = ${secretsJSON};
      const __envApi = {
        get(key) { return __secrets[key]; },
        has(key) { return Object.prototype.hasOwnProperty.call(__secrets, key); },
        toObject() { return { ...__secrets }; },
        set() { /* read-only inside the worker */ },
        delete() { /* read-only */ },
      };
      try {
        Object.defineProperty(globalThis.Deno, 'env', {
          value: __envApi, enumerable: true, configurable: true, writable: false,
        });
      } catch (e) {
        // Older Deno builds may have frozen Deno; falling back to a worker
        // crash is the right behaviour — the bundler contract assumes the
        // secret surface is in place before the user module runs.
        throw new Error('failed to install Deno.env shim in worker: ' + (e && e.message || e));
      }
      try {
        Object.defineProperty(globalThis.Deno, 'serve', {
          value: () => {
            throw new Error('Deno.serve is not available inside Excalibase functions — export a default handler instead');
          },
          enumerable: true, configurable: true, writable: false,
        });
      } catch (_e) { /* non-fatal; serve is rare in user code */ }
      // Plain env namespace mirrors the api for code that does not use
      // Deno.env.* directly. Configurable=true so test injections can
      // re-define if needed.
      Object.defineProperty(globalThis, 'env', {
        value: __envApi, enumerable: true, configurable: true, writable: false,
      });
    })();

    // --- outbound refusals ---
    // A host outside the egress allowlist fails with Deno's NotCapable; the
    // error keeps its class and gets a message naming the Studio setting.
    (function() {
      const __deniedMessage = ${outboundDeniedMessage.toString()};
      const __told = (e) => {
        const message = e instanceof Deno.errors.NotCapable ? __deniedMessage(String(e.message)) : null;
        return message ? new Deno.errors.NotCapable(message) : e;
      };
      const __wrapAsync = (fn, self) => async function(...args) {
        try { return await fn.apply(self, args); } catch (e) { throw __told(e); }
      };
      globalThis.fetch = __wrapAsync(globalThis.fetch, globalThis);
      for (const name of ['connect', 'connectTls']) {
        try {
          Object.defineProperty(globalThis.Deno, name, {
            value: __wrapAsync(globalThis.Deno[name], globalThis.Deno),
            enumerable: true, configurable: true, writable: false,
          });
        } catch (_e) { /* the original refusal still applies */ }
      }
      const __WebSocket = globalThis.WebSocket;
      globalThis.WebSocket = class WebSocket extends __WebSocket {
        constructor(...args) {
          try { super(...args); } catch (e) { throw __told(e); }
        }
      };
    })();

    // --- load user module ---
    // The bundle is delivered as a single string (ESM by default in
    // Phase 9b.F; legacy script-style globalThis.__excalibase_default = X
    // bundles also work because dynamic import evaluates the module body
    // and any side-effect assignment to globalThis survives). The Blob's
    // MIME type tells Deno to strip TypeScript annotations on parse — the
    // Go bundler already targets ES2022 JS, but inline test bundles in
    // the harness rely on the parser tolerating TS syntax.
    {
      const __userCode = ${JSON.stringify(userCode)};
      const __userBlob = new Blob([__userCode], { type: 'application/javascript' });
      const __userUrl = URL.createObjectURL(__userBlob);
      try {
        const __userModule = await import(__userUrl);
        // Prefer the ESM default export when present; fall back to the
        // legacy global slot for test and historical bundles that did not
        // use export default.
        if (__userModule && __userModule.default !== undefined &&
            globalThis.__excalibase_default === undefined) {
          globalThis.__excalibase_default = __userModule.default;
        }
      } catch (__importErr) {
        // The user module failed to load (parse error, unresolvable npm
        // specifier, etc.). We MUST NOT let this bubble as an unhandled
        // rejection — Deno tears the whole worker (and on some versions
        // the parent process) down for unhandled errors in module
        // evaluation. Post a structured boot-error message back to main
        // and re-throw inside the worker so onerror fires cleanly on the
        // main side. The main thread's deploy() promise rejects with the
        // captured message.
        const __msg = (__importErr && __importErr.message) || String(__importErr);
        try {
          self.postMessage({ type: 'bootError', error: __msg });
        } catch (_postErr) { /* ignore */ }
        throw new Error('user module failed to load: ' + __msg);
      } finally {
        // Best-effort revoke so the Blob URL doesn't pin its bytes.
        try { URL.revokeObjectURL(__userUrl); } catch (_e) { /* ignore */ }
      }
    }

    // --- v2 helpers (only used when EXCALIBASE_FUNCTIONS_V2 is on) ---
    const __V2_ENABLED = ${V2_ENABLED ? "true" : "false"};
    const __V2_KINDS = ${JSON.stringify(V2_KINDS)};

    // __isV2Export — duck-types a tagged FunctionDef record:
    //   { kind: "query"|"mutation"|"action", args: <zod-or-parseable>, handler: function }
    // or, for Phase 7's HTTP shapes:
    //   { kind: "httpAction", handler: function }
    //   { kind: "httpRouter", __excalibase_route_handlers: { ... } }
    // We accept the args-carrying kinds whose 'args' is non-null because
    // the user's zod (or hand-rolled) parser will run inside the handler
    // shim below. http* kinds skip the args validation entirely.
    function __isV2Export(d) {
      if (!d || typeof d !== 'object') return false;
      if (__V2_KINDS.indexOf(d.kind) === -1) return false;
      if (d.kind === 'httpAction') {
        return typeof d.handler === 'function';
      }
      if (d.kind === 'httpRouter') {
        // The router itself has no callable handler — dispatch resolves
        // per-route handlers from __excalibase_route_handlers. We just
        // confirm the side-channel map is present.
        return typeof d.__excalibase_route_handlers === 'object' &&
               d.__excalibase_route_handlers !== null;
      }
      if (typeof d.handler !== 'function') return false;
      if (d.args === null || d.args === undefined) return false;
      return true;
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

    // __verifiedClaims — claims only from a token the gateway verified. The
    // gateway sets X-Excalibase-Auth-Verified: 1 after checking the token's
    // signature and project, and drops any marker the caller sent. A function
    // that skips verification still sees the Authorization header itself,
    // never as ctx.auth (EXC-518).
    // __postThrown answers a handler's throw. A FunctionError (matched by name,
    // as the lib's class lives in the bundle) refuses with its 4xx status;
    // anything else is a crash: logged here, and marked failed so the caller
    // of /invoke sees a bare 500 while a nested ctx.run* caller keeps the text.
    function __postThrown(reqId, err) {
      const status = err && err.name === 'FunctionError' ? err.status : 0;
      if (Number.isInteger(status) && status >= 400 && status <= 499) {
        self.postMessage({ type: 'success', reqId, status,
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ error: String(err.message) }) });
        return;
      }
      console.error('function failed:', err);
      self.postMessage({ type: 'success', reqId, status: 500, failed: true,
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ error: String(err && err.message || err) }) });
    }

    function __verifiedClaims(headers) {
      const marker = headers['X-Excalibase-Auth-Verified'] || headers['x-excalibase-auth-verified'];
      if (marker !== '1') return null;
      return __decodeJwtClaims(headers['Authorization'] || headers['authorization'] || '');
    }

    // __buildAuthCtx — Phase 12: typed ctx.auth surface mirroring Convex's
    // Auth interface. Carries the legacy raw claims plus a typed
    // getUserIdentity() helper that surfaces standard OIDC claims under
    // their Convex-documented names (sub -> subject, iss -> issuer, email,
    // email_verified -> emailVerified, picture -> pictureUrl, name,
    // given_name, family_name, etc.). Custom claims pass through via the
    // spread so handler code can access them by their JWT key.
    //
    // Returns a fresh object per invocation so the helper closes over this
    // request's claims; the per-call cost is negligible compared to the
    // worker RPC overhead.
    function __buildAuthCtx(claims) {
      return {
        claims: claims,
        getUserIdentity: async () => {
          if (!claims || typeof claims !== 'object') return null;
          const sub = typeof claims.sub === 'string' ? claims.sub : '';
          const iss = typeof claims.iss === 'string' ? claims.iss : '';
          // Build the identity by spreading the raw claims first (so
          // custom claims surface under their JWT keys), then overlaying
          // the Convex-shape rename / type-coerced versions of the
          // standard OIDC slots. The spread is shallow — that's enough
          // because OIDC claims are scalar.
          return Object.assign({}, claims, {
            tokenIdentifier: iss + '|' + sub,
            subject: sub,
            issuer: iss,
            name: typeof claims.name === 'string' ? claims.name : undefined,
            email: typeof claims.email === 'string' ? claims.email : undefined,
            emailVerified: typeof claims.email_verified === 'boolean'
              ? claims.email_verified : undefined,
            phoneNumber: typeof claims.phone_number === 'string'
              ? claims.phone_number : undefined,
            phoneNumberVerified: typeof claims.phone_number_verified === 'boolean'
              ? claims.phone_number_verified : undefined,
            pictureUrl: typeof claims.picture === 'string' ? claims.picture : undefined,
            givenName: typeof claims.given_name === 'string' ? claims.given_name : undefined,
            familyName: typeof claims.family_name === 'string' ? claims.family_name : undefined,
            nickname: typeof claims.nickname === 'string' ? claims.nickname : undefined,
            preferredUsername: typeof claims.preferred_username === 'string'
              ? claims.preferred_username : undefined,
            profileUrl: typeof claims.profile === 'string' ? claims.profile : undefined,
            updatedAt: typeof claims.updated_at === 'string' ? claims.updated_at : undefined,
            birthday: typeof claims.birthday === 'string' ? claims.birthday : undefined,
            gender: typeof claims.gender === 'string' ? claims.gender : undefined,
            language: typeof claims.locale === 'string' ? claims.locale : undefined,
            timezone: typeof claims.zoneinfo === 'string' ? claims.zoneinfo : undefined,
          });
        },
      };
    }

    // --- ctx.db (removed in @excalibase/server 0.11.0) ---
    // Any use names what to do instead, rather than failing on a null.
    const __removedDbMessage = "ctx.db is not available: read and write your project's data through its GraphQL or REST API with a project key";
    // Symbol keys, then and toJSON pass, so inspecting, awaiting or
    // serializing a ctx still works; any real use throws.
    const __removedDb = new Proxy({}, {
      get(_target, key) {
        if (typeof key === 'symbol' || key === 'then' || key === 'toJSON') return undefined;
        throw new Error(__removedDbMessage);
      },
    });
    // Each invocation's ctx closes over its own call context
    // ({ txnRefId, depth }), so overlapping invocations on one worker never
    // see each other's mutation scope or run depth. txnRefId is empty
    // outside a mutation.

    // --- ctx.runQuery / runMutation / runAction (Phase 7) ---
    // Composition surface for cross-function calls. Each call posts a
    // {type:'runX', op, ref, args} message to the main thread; the main
    // thread looks up the target (in-process if same runtime, HTTP self-call
    // if not) and posts {type:'runXResult', rpcId, result} back.
    //
    // The current call depth rides along in the envelope so the dispatcher
    // can reject runaway recursion before the message reaches the target.
    // The initial depth comes from the invocation's call context, and
    // increments for every nested call made via __runXCall.
    const __runXPending = new Map();
    let __nextRunXRpcId = 1;
    const __RUN_MAX_DEPTH = ${RUN_MAX_DEPTH};

    function __runXCall(op, ref, args, call) {
      // ref must look like { moduleName, exportName }. Reject malformed
      // values up-front so the user gets a clean error.
      if (!ref || typeof ref !== 'object' ||
          typeof ref.moduleName !== 'string' || ref.moduleName.length === 0 ||
          typeof ref.exportName !== 'string' || ref.exportName.length === 0) {
        return Promise.reject(new Error("ctx." + op + ": first argument must be a FunctionRef { moduleName, exportName }"));
      }
      const nextDepth = call.depth + 1;
      if (nextDepth > __RUN_MAX_DEPTH) {
        return Promise.reject(new Error(
          "ctx." + op + ": run depth limit exceeded (" + __RUN_MAX_DEPTH +
          "). Check for infinite recursion."
        ));
      }
      const rpcId = __nextRunXRpcId++;
      return new Promise((resolve, reject) => {
        __runXPending.set(rpcId, { resolve, reject });
        self.postMessage({
          type: 'runX', rpcId, op, ref, args,
          depth: nextDepth,
          // Forward the caller's scope ref so a mutation→mutation call joins
          // the parent's scope. Main-thread dispatch only honours this when
          // the caller's kind is "mutation".
          txnRefId: call.txnRefId,
        });
      });
    }

    function __makeRunQuery(call)    { return (ref, args) => __runXCall('runQuery',    ref, args, call); }
    function __makeRunMutation(call) { return (ref, args) => __runXCall('runMutation', ref, args, call); }
    function __makeRunAction(call)   { return (ref, args) => __runXCall('runAction',   ref, args, call); }

    // --- ctx.scheduler (Phase 8) ---
    // Worker-side facade for runAfter / runAt / cancel. Each method posts
    // a {type:'scheduler', op, ...} message to the main thread, which sends
    // it to provisioning and posts {type:'schedulerResult', rpcId, result}.
    const __schedPending = new Map();
    let __nextSchedRpcId = 1;
    function __schedCall(op, payload, call) {
      const rpcId = __nextSchedRpcId++;
      return new Promise((resolve, reject) => {
        __schedPending.set(rpcId, { resolve, reject });
        // Inside a mutation the call waits for the mutation to succeed.
        // Action handlers carry no ref, so their schedule is sent at once.
        self.postMessage({ type: 'scheduler', rpcId, op, payload,
          txnRefId: call.txnRefId });
      });
    }
    function __makeScheduler(call) {
      return {
        runAfter: (delayMs, ref, args) => {
          if (!ref || typeof ref !== 'object' ||
              typeof ref.moduleName !== 'string' || ref.moduleName.length === 0 ||
              typeof ref.exportName !== 'string' || ref.exportName.length === 0) {
            return Promise.reject(new Error("ctx.scheduler.runAfter: ref must be { moduleName, exportName }"));
          }
          const d = Number(delayMs);
          if (!Number.isFinite(d)) {
            return Promise.reject(new Error("ctx.scheduler.runAfter: delayMs must be a finite number"));
          }
          return __schedCall('runAfter', { delayMs: d, ref, args: args === undefined ? {} : args }, call);
        },
        runAt: (timestamp, ref, args) => {
          if (!ref || typeof ref !== 'object' ||
              typeof ref.moduleName !== 'string' || ref.moduleName.length === 0 ||
              typeof ref.exportName !== 'string' || ref.exportName.length === 0) {
            return Promise.reject(new Error("ctx.scheduler.runAt: ref must be { moduleName, exportName }"));
          }
          const t = Number(timestamp);
          if (!Number.isFinite(t)) {
            return Promise.reject(new Error("ctx.scheduler.runAt: timestamp must be a finite number"));
          }
          return __schedCall('runAt', { timestamp: t, ref, args: args === undefined ? {} : args }, call);
        },
        cancel: (id) => {
          if (typeof id !== 'string' || id.length === 0) {
            return Promise.reject(new Error("ctx.scheduler.cancel: id must be a non-empty string"));
          }
          return __schedCall('cancel', { id }, call);
        },
      };
    }

    // --- ctx.storage (Phase 10) ---
    // Worker-side facade for the Convex file-storage surface. Mirrors
    // https://docs.convex.dev/file-storage. Every op posts a
    // {type:'storage', op, payload} message to the main thread, which
    // translates it into an HTTP call against provisioning's
    // /internal/storage/{projectId}/* routes (runtime-token auth) and
    // posts {type:'storageResult', rpcId, result} back.
    //
    // QueryCtx gets a read-only surface (getUrl/get/getMetadata);
    // MutationCtx and ActionCtx get the full read+write surface adding
    // generateUploadUrl/store/delete.
    const __storagePending = new Map();
    let __nextStorageRpcId = 1;
    function __storageCall(op, payload) {
      const rpcId = __nextStorageRpcId++;
      return new Promise((resolve, reject) => {
        __storagePending.set(rpcId, { resolve, reject });
        self.postMessage({ type: 'storage', rpcId, op, payload });
      });
    }
    function __assertStorageId(id, who) {
      if (typeof id !== 'string' || id.length === 0) {
        throw new Error(who + ": storageId must be a non-empty string");
      }
    }
    function __makeStorageReader() {
      return {
        async getUrl(id) {
          __assertStorageId(id, "ctx.storage.getUrl");
          const reply = await __storageCall('getUrl', { storageId: id });
          if (reply === null || reply === undefined) return null;
          if (typeof reply !== 'string') {
            throw new Error("ctx.storage.getUrl: unexpected RPC reply type");
          }
          return reply;
        },
        async get(id) {
          __assertStorageId(id, "ctx.storage.get");
          const reply = await __storageCall('get', { storageId: id });
          if (reply === null || reply === undefined) return null;
          // reply.bytes is a Uint8Array (structured-clone preserves it);
          // reply.contentType is the persisted content type.
          return new Blob([reply.bytes], { type: reply.contentType || 'application/octet-stream' });
        },
        async getMetadata(id) {
          __assertStorageId(id, "ctx.storage.getMetadata");
          const reply = await __storageCall('getMetadata', { storageId: id });
          if (reply === null || reply === undefined) return null;
          return reply;
        },
      };
    }
    function __makeStorageWriter() {
      const reader = __makeStorageReader();
      return Object.assign({}, reader, {
        async generateUploadUrl(declaration) {
          // The signed PUT binds the type and the length, so the client must
          // send exactly what is declared here; neither can be guessed later.
          const contentType = (declaration && declaration.contentType) || '';
          const size = (declaration && declaration.size) || 0;
          if (typeof contentType !== 'string' || contentType === '') {
            throw new Error("ctx.storage.generateUploadUrl: contentType is required");
          }
          if (typeof size !== 'number' || !Number.isFinite(size) || size <= 0) {
            throw new Error("ctx.storage.generateUploadUrl: size is required and must be greater than zero");
          }
          const reply = await __storageCall('generateUploadUrl', { contentType, size });
          if (!reply || typeof reply.url !== 'string') {
            throw new Error("ctx.storage.generateUploadUrl: unexpected RPC reply");
          }
          if (typeof reply.storageId !== 'string' || reply.storageId === '') {
            throw new Error("ctx.storage.generateUploadUrl: reply carried no storageId");
          }
          if (typeof reply.uploadId !== 'string' || reply.uploadId === '') {
            throw new Error("ctx.storage.generateUploadUrl: reply carried no uploadId");
          }
          return { url: reply.url, storageId: reply.storageId, uploadId: reply.uploadId };
        },
        async completeUpload(completion) {
          // A direct upload sits in a staging area until something accepts
          // it. The browser cannot reach the internal route, so a function
          // does — and an upload nobody confirms is collected after the grace.
          const storageId = (completion && completion.storageId) || '';
          const uploadId = (completion && completion.uploadId) || '';
          if (typeof storageId !== 'string' || storageId === '') {
            throw new Error("ctx.storage.completeUpload: storageId is required");
          }
          if (typeof uploadId !== 'string' || uploadId === '') {
            throw new Error("ctx.storage.completeUpload: uploadId is required");
          }
          const reply = await __storageCall('completeUpload', { storageId, uploadId });
          if (typeof reply !== 'string' || reply.length === 0) {
            throw new Error("ctx.storage.completeUpload: RPC did not return a storage id");
          }
          return reply;
        },
        async store(blob, opts) {
          if (!blob || typeof blob.arrayBuffer !== 'function') {
            throw new Error("ctx.storage.store: blob argument is required");
          }
          const buf = await blob.arrayBuffer();
          const bytes = new Uint8Array(buf);
          const reply = await __storageCall('store', {
            bytes,
            contentType: blob.type || 'application/octet-stream',
            sha256: opts && opts.sha256 ? opts.sha256 : undefined,
          });
          if (typeof reply !== 'string' || reply.length === 0) {
            throw new Error("ctx.storage.store: RPC did not return a storage id");
          }
          return reply;
        },
        async delete(id) {
          __assertStorageId(id, "ctx.storage.delete");
          await __storageCall('delete', { storageId: id });
        },
      });
    }

    // __dispatchHttp — Phase 7 httpAction/httpRouter dispatch. Skips the
    // {args}-parsing v2 path; reconstructs a raw Request from the invoke
    // envelope and forwards the handler's Response unchanged.
    //
    // For an httpAction the default export's handler runs every time.
    // For an httpRouter we look up the matching (path, method) row in
    // the bundled __excalibase_route_handlers map; misses return 404.
    async function __dispatchHttp(reqId, reqData, fnDef, txnRefId) {
      try {
        const headers = reqData.headers || {};
        const claims = __verifiedClaims(headers);
        // httpAction/httpRouter don't have an args envelope, so the runX
        // envelope's depth must ride alongside reqData.runDepth (the
        // gateway forwards it that way for internal invocations).
        // Public traffic has no depth, so default to 0.
        const incomingDepth = typeof reqData.runDepth === 'number' ? reqData.runDepth : 0;
        // http* handlers own no scope (action-equivalent boundary). Forward
        // any incoming ref so a nested ctx.runMutation routes correctly.
        const call = { txnRefId: typeof txnRefId === 'string' ? txnRefId : '', depth: incomingDepth };

        // Reconstruct a raw Request. The runtime accepts relative URLs;
        // Request requires an absolute one. Use the same synthetic base
        // as the legacy Fetch path so URL parsing inside the handler
        // (req.url, new URL(req.url)) works.
        let url = reqData.url || '/';
        if (!/^https?:\/\//.test(url)) {
          url = 'http://fn.excalibase.local' + (url.startsWith('/') ? '' : '/') + url;
        }
        const init = { method: reqData.method || 'GET', headers };
        if (reqData.body && reqData.method !== 'GET' && reqData.method !== 'HEAD') {
          init.body = reqData.body;
        }
        const req = new Request(url, init);

        // httpAction ctx parity: db is null (Convex contract), runX
        // surface is wired so the handler can compose with other functions.
        // Scheduler is also present — httpAction handlers can enqueue
        // background work the same way actions can. Phase 10: storage
        // exposed as a writer (httpAction behaves like an ActionCtx).
        const ctx = {
          db: __removedDb,
          auth: __buildAuthCtx(claims),
          runQuery:    __makeRunQuery(call),
          runMutation: __makeRunMutation(call),
          runAction:   __makeRunAction(call),
          scheduler:   __makeScheduler(call),
          storage:     __makeStorageWriter(),
        };

        // Find the handler. For httpAction the def itself carries it; for
        // httpRouter we resolve via the route-handlers map keyed by
        // "METHOD path". A miss is a 404 (Convex parity: method mismatch
        // is also 404, not 405).
        let handler;
        if (fnDef.kind === 'httpAction') {
          handler = fnDef.handler;
        } else if (fnDef.kind === 'httpRouter') {
          const handlers = fnDef.__excalibase_route_handlers || {};
          const path = new URL(req.url).pathname;
          handler = handlers[req.method + ' ' + path];
          if (typeof handler !== 'function') {
            self.postMessage({ type: 'success', reqId, status: 404,
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ error: 'not found' }) });
            return;
          }
        }
        if (typeof handler !== 'function') {
          self.postMessage({ type: 'success', reqId, status: 500,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ error: 'http handler missing' }) });
          return;
        }

        let res;
        try {
          res = await handler(ctx, req);
        } catch (err) {
          __postThrown(reqId, err);
          return;
        }
        if (!(res instanceof Response)) {
          self.postMessage({ type: 'success', reqId, status: 500,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ error: 'http handler must return a Response' }) });
          return;
        }
        const bodyText = await res.text();
        const outHeaders = {};
        res.headers.forEach((v, k) => { outHeaders[k] = v; });
        self.postMessage({ type: 'success', reqId, status: res.status,
          headers: outHeaders, body: bodyText });
      } catch (outer) {
        self.postMessage({ type: 'error', reqId,
          error: String(outer && outer.message || outer) });
      }
    }

    // __dispatchV2 — runs the tagged FunctionDef contract:
    //   POST body must be { "args": <object> }; on missing args, 400.
    //   Calls handler(ctx, body.args); wraps result as { data } JSON.
    //   ctx.db refuses use on every kind (removed in 0.11.0).
    // The def's zod schema guards the handler: refused args never reach it
    // (EXC-560). A hand-written def without safeParse passes args through.
    function __checkArgs(fnDef, args) {
      if (!fnDef.args || typeof fnDef.args.safeParse !== 'function') return { ok: true, args };
      const parsed = fnDef.args.safeParse(args);
      if (parsed && parsed.success === true) return { ok: true, args: parsed.data };
      const issues = parsed && parsed.error && Array.isArray(parsed.error.issues) ? parsed.error.issues : [];
      return { ok: false, issues };
    }

    async function __dispatchV2(reqId, reqData, fnDef, txnRefId) {
      try {
        const headers = reqData.headers || {};
        const claims = __verifiedClaims(headers);

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

        // Initial depth comes from the request body (set by dispatchRunX
        // for cross-function calls; absent on top-level public traffic).
        // The gateway sets it to 0 for top-level public calls; runX bumps
        // it for each nested call so the chain is end-to-end bounded.
        const incomingDepth = typeof body.runDepth === 'number' ? body.runDepth : 0;
        // The scope ref the main thread stamped on this invocation; empty
        // outside a mutation (query, action or v1 fetch handler).
        const call = { txnRefId: typeof txnRefId === 'string' ? txnRefId : '', depth: incomingDepth };
        const ctx = {
          db: __removedDb,
          auth: __buildAuthCtx(claims),
          // Phase 7: composition surface. Type-level read-only rules are
          // enforced by the lib (QueryCtx has no runMutation/runAction);
          // the worker exposes all three on every Ctx variant so a
          // hand-rolled bundle can still compose, but discipline lives
          // in the typed lib path. Read-only enforcement at runtime is
          // the main thread's job (dispatchRunX kind-matrix check).
          runQuery:    __makeRunQuery(call),
          runMutation: __makeRunMutation(call),
          runAction:   __makeRunAction(call),
          // Phase 8: scheduler — present on mutation/action ctx via the
          // typed lib. Worker exposes it on every Ctx so the runtime
          // path is uniform; the typed lib still keeps it off QueryCtx
          // (a query handler that tries to schedule is a compile error).
          scheduler:   __makeScheduler(call),
          // Phase 10: ctx.storage — kind-aware. QueryCtx gets the
          // read-only surface so untyped bundles that try to call
          // generateUploadUrl/store/delete from a query throw a clear
          // "method missing" error at runtime (matches the type-level
          // rejection enforced by @excalibase/server). Mutations and
          // actions get the full writer surface.
          storage:     fnDef.kind === 'query'
            ? __makeStorageReader()
            : __makeStorageWriter(),
        };
        const checked = __checkArgs(fnDef, body.args);
        if (!checked.ok) {
          self.postMessage({ type: 'success', reqId, status: 400,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ error: 'args validation failed', issues: checked.issues }) });
          return;
        }
        const args = checked.args;
        try {
          const result = await fnDef.handler(ctx, args);
          self.postMessage({ type: 'success', reqId, status: 200,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ data: result === undefined ? null : result }) });
        } catch (err) {
          // Surface ValidationError from @excalibase/server as a 400.
          if (err && err.name === 'ValidationError' && Array.isArray(err.issues)) {
            self.postMessage({ type: 'success', reqId, status: 400,
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ error: 'validation', issues: err.issues }) });
            return;
          }
          // If the handler threw a zod-style issues array, relay it as 400.
          if (err && Array.isArray(err.issues)) {
            self.postMessage({ type: 'success', reqId, status: 400,
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ error: 'args validation failed', issues: err.issues }) });
            return;
          }
          __postThrown(reqId, err);
        }
      } catch (outer) {
        self.postMessage({ type: 'error', reqId, error: String(outer && outer.message || outer) });
      }
    }

    // --- v2 export metadata scan + report ---
    // After the user module loaded above, scan globalThis.__excalibase_default
    // for the v2 tagged shape and build a name/kind/argsJsonSchema array.
    // Since this phase has a single default export per function, the array
    // has at most one entry. The export's __metadata.argsJsonSchema (set by
    // the @excalibase/server tag wrappers when they call zodToJsonSchema)
    // is preferred; we fall back to an empty schema when none is attached.
    function __collectV2Metadata() {
      if (!__V2_ENABLED) return [];
      const exp = globalThis.__excalibase_default;
      if (!__isV2Export(exp)) return [];
      const meta = exp.__metadata || {};
      const argsJsonSchema =
        (meta && typeof meta.argsJsonSchema === 'object' && meta.argsJsonSchema !== null)
          ? meta.argsJsonSchema
          : { type: 'object', properties: {} };
      // The name field is filled by the main thread (it knows the runtime
      // id) before the HTTP forward — here we report a placeholder.
      const entry = { name: 'default', kind: exp.kind, argsJsonSchema: argsJsonSchema };
      // Phase 7: surface the isInternal flag so the gateway can gate
      // PublicInvoke. Omit when false to keep the metadata payload
      // byte-stable for legacy v2 records.
      if (exp.isInternal === true) entry.isInternal = true;
      return [entry];
    }

    // --- worker dispatch ---
    // Each invoke message carries a unique reqId so the runtime can correlate
    // concurrent responses on the same worker without races. The same
    // handler also routes RPC replies back to their pending calls.
    self.onmessage = async (e) => {
      const msg = e.data;
      // Phase 7: runX RPC reply.
      if (msg && msg.type === 'runXResult') {
        const pending = __runXPending.get(msg.rpcId);
        if (!pending) return;
        __runXPending.delete(msg.rpcId);
        if (msg.result && msg.result.ok === true) {
          pending.resolve(msg.result.data);
        } else {
          const err = new Error(msg.result && msg.result.error || 'runX error');
          if (msg.result && msg.result.issues) {
            err.issues = msg.result.issues;
            err.name = msg.result.errorName || 'ValidationError';
          } else if (msg.result && msg.result.errorName) {
            err.name = msg.result.errorName;
          }
          if (msg.result && typeof msg.result.status === 'number') err.status = msg.result.status;
          pending.reject(err);
        }
        return;
      }
      // Phase 8: scheduler RPC reply.
      if (msg && msg.type === 'schedulerResult') {
        const pending = __schedPending.get(msg.rpcId);
        if (!pending) return;
        __schedPending.delete(msg.rpcId);
        if (msg.result && msg.result.ok === true) {
          pending.resolve(msg.result.data);
        } else {
          pending.reject(new Error(msg.result && msg.result.error || 'scheduler error'));
        }
        return;
      }
      // Phase 10: storage RPC reply.
      if (msg && msg.type === 'storageResult') {
        const pending = __storagePending.get(msg.rpcId);
        if (!pending) return;
        __storagePending.delete(msg.rpcId);
        if (msg.result && msg.result.ok === true) {
          pending.resolve(msg.result.data);
        } else {
          pending.reject(new Error(msg.result && msg.result.error || 'storage error'));
        }
        return;
      }
      if (msg && msg.type === 'invoke') {
        const reqId = msg.reqId;
        const reqData = msg.data;
        // The main thread stamps the mutation-scope ref onto every invoke
        // envelope so this worker's scheduler/runX RPCs can echo it back.
        // Empty outside a mutation.
        const txnRefId = typeof msg.txnRefId === 'string' ? msg.txnRefId : '';

        // v2 path — only if the flag is on AND the export matches the
        // tagged FunctionDef shape. Anything else falls through to legacy.
        const exp = globalThis.__excalibase_default;
        if (__V2_ENABLED && __isV2Export(exp)) {
          // Phase 7: httpAction / httpRouter take the raw-Request path.
          if (exp.kind === 'httpAction' || exp.kind === 'httpRouter') {
            await __dispatchHttp(reqId, reqData, exp, txnRefId);
            return;
          }
          await __dispatchV2(reqId, reqData, exp, txnRefId);
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
    // Best-effort metadata emission. Captured before 'ready' so main can
    // forward the payload as soon as deploy finishes; failures are
    // swallowed so a malformed user export never blocks deploy.
    try {
      const __exports = __collectV2Metadata();
      if (__exports.length > 0) {
        self.postMessage({ type: 'metadata', exports: __exports });
      }
    } catch (_metaErr) { /* ignore */ }

    self.postMessage({ type: 'ready' });
  `;
}

// RunXMessage is the envelope shape posted by a worker's __runXCall(...)
// surface back to the main thread. `op` is the variant being invoked,
// `ref` carries the typed function reference, `args` is the validated
// payload, and `depth` is the post-increment composition depth (the
// dispatcher enforces depth ≤ RUN_MAX_DEPTH before forwarding).
interface RunXMessage {
  type: "runX";
  rpcId: number;
  op: "runQuery" | "runMutation" | "runAction";
  ref: { moduleName: string; exportName: string };
  args: unknown;
  depth: number;
  // Present when the caller runs in a mutation scope. dispatchRunX only
  // forwards it when the kind matrix permits it (mutation → mutation).
  txnRefId?: string;
}

// dispatchRunX resolves a function reference against the runtime's local
// script table and invokes the target in-process. The caller's runtime id
// (`${projectId}__${fnId}`) gives us the project; we look for any deployed
// script in the same project whose fn-id matches the ref's moduleName.
//
// For Phase 7 the in-process path is the only one wired here. Cross-runtime
// hops can be added in Phase 8 (scheduler) when separate pods need to
// compose — the gateway's /internal/invoke route is ready to receive them.
async function dispatchRunX(callerRuntimeID: string, msg: RunXMessage): Promise<unknown> {
  if (msg.depth > RUN_MAX_DEPTH) {
    throw new Error(`ctx.${msg.op}: run depth limit exceeded (${RUN_MAX_DEPTH})`);
  }
  const sep = callerRuntimeID.indexOf("__");
  if (sep < 0) {
    throw new Error("runX: caller runtime id missing project separator");
  }
  const projectID = callerRuntimeID.slice(0, sep);
  const targetID = `${projectID}__${msg.ref.moduleName}`;

  // Phase 8 read-only enforcement. The caller and target kinds are
  // captured at deploy time via the metadata callback (see
  // ScriptMetadata.kind). We refuse the call before dispatching when the
  // matrix says "no":
  //
  //   query   → query      OK
  //   query   → mutation   ERROR
  //   query   → action     ERROR
  //   mutation→ query      OK
  //   mutation→ mutation   OK (joins the caller's scope)
  //   mutation→ action     OK
  //   action  → *          OK
  //
  // Unknown caller or target kinds are not enforced — falls back to the
  // pre-Phase-8 behaviour so v1 fetch handlers (which never report a
  // kind) keep working.
  const callerKind = runtime.getKind(callerRuntimeID);
  const targetKind = runtime.getKind(targetID);
  if (callerKind === "query") {
    if (targetKind === "mutation") {
      throw new Error(
        `Calling mutation ${msg.ref.moduleName} from a query is not allowed`,
      );
    }
    if (targetKind === "action") {
      throw new Error(
        `Calling action ${msg.ref.moduleName} from a query is not allowed`,
      );
    }
  }
  // Forward the caller's scope ref only from a mutation to a mutation; an
  // action's ctx.runMutation is a new top level with its own scope.
  const propagateTxn = callerKind === "mutation" && targetKind === "mutation"
    && typeof msg.txnRefId === "string" && msg.txnRefId.length > 0;
  const bodyPayload: Record<string, unknown> = {
    args: msg.args,
    runDepth: msg.depth,
  };
  if (propagateTxn) bodyPayload.txnRefId = msg.txnRefId;
  const invokeReq: InvokeRequest = {
    method: "POST",
    url: `/invoke/${targetID}`,
    headers: { "content-type": "application/json" },
    body: JSON.stringify(bodyPayload),
  };
  let res: InvokeResponse;
  try {
    res = await runtime.invoke(targetID, invokeReq);
  } catch (err) {
    throw err instanceof Error ? err : new Error(String(err));
  }
  // The target's response envelope mirrors the dispatchV2 contract:
  //   200 + { data } on success, 400/500 + { error, issues? } on failure.
  let parsed: { data?: unknown; error?: string; issues?: unknown };
  try {
    parsed = JSON.parse(res.body);
  } catch (_) {
    parsed = { error: res.body };
  }
  if (res.status >= 200 && res.status < 300) {
    return parsed.data === undefined ? null : parsed.data;
  }
  throw runXTargetError(res.status, parsed);
}

// SchedulerMessage is the envelope shape posted by a worker's
// `ctx.scheduler.*` calls. Mirrors RunXMessage but on its own type so
// dispatch is unambiguous.
interface SchedulerMessage {
  type: "scheduler";
  rpcId: number;
  op: "runAfter" | "runAt" | "cancel";
  payload: {
    delayMs?: number;
    timestamp?: number;
    ref?: { moduleName: string; exportName: string };
    args?: unknown;
    id?: string;
  };
}

/**
 * SchedulerOp is one ctx.scheduler call as provisioning records it. The
 * runtime never writes the scheduler's tables itself: they belong to the
 * platform, and the project's own login has no write path into them
 * (EXC-623). Provisioning writes them as the scheduler's login.
 */
type SchedulerOp =
  | {
    op: "schedule";
    id: string;
    moduleName: string;
    exportName: string;
    args: unknown;
    scheduledForMs: number;
  }
  | { op: "cancel"; id: string };

/**
 * dispatchScheduler turns one `ctx.scheduler.*` call into a SchedulerOp and
 * resolves with what the worker's promise receives (a `ScheduledId` for
 * `runAfter`/`runAt`, `null` for `cancel`). Inside a mutation the op waits
 * for the mutation to succeed; anywhere else it is sent at once, so an
 * action's schedule stands whatever the action does next (Convex parity).
 */
async function dispatchScheduler(
  callerRuntimeID: string,
  msg: SchedulerMessage,
  txnRefId: string | undefined,
): Promise<unknown> {
  const sep = callerRuntimeID.indexOf("__");
  if (sep < 0) {
    throw new Error("scheduler: caller runtime id missing project separator");
  }
  const projectID = callerRuntimeID.slice(0, sep);
  const op = schedulerOpFor(msg);
  if (txnRefId) {
    if (!mutationScopes.add(txnRefId, projectID, op)) {
      throw new Error("ctx.scheduler: the mutation that made this call has already ended");
    }
  } else {
    await sendSchedulerOps(projectID, [op]);
  }
  return op.op === "schedule" ? op.id : null;
}

function schedulerOpFor(msg: SchedulerMessage): SchedulerOp {
  if (msg.op === "cancel") {
    const id = msg.payload.id;
    if (typeof id !== "string" || id.length === 0) {
      throw new Error("scheduler.cancel: id required");
    }
    return { op: "cancel", id };
  }
  if (msg.op !== "runAfter" && msg.op !== "runAt") {
    throw new Error(`scheduler: unknown op ${msg.op}`);
  }
  const ref = msg.payload.ref;
  if (!ref || typeof ref.moduleName !== "string" || typeof ref.exportName !== "string") {
    throw new Error(`scheduler.${msg.op}: missing ref`);
  }
  const scheduledForMs = msg.op === "runAfter"
    ? Date.now() + Math.max(0, Number(msg.payload.delayMs ?? 0))
    : Number(msg.payload.timestamp ?? Date.now());
  if (!Number.isFinite(scheduledForMs)) {
    throw new TypeError(`scheduler.${msg.op}: time is not a number`);
  }
  return {
    op: "schedule",
    id: newId(),
    moduleName: ref.moduleName,
    exportName: ref.exportName,
    args: msg.payload.args ?? {},
    scheduledForMs: Math.trunc(scheduledForMs),
  };
}

/**
 * callProvisioning performs a fetch to the platform and, on a network-level
 * failure, logs the detail (which can name the internal endpoint) and throws
 * a fixed message, so customer function code never receives the URL
 * (EXC-623). HTTP error statuses are left for the caller to handle.
 */
async function callProvisioning(label: string, url: string, init?: RequestInit): Promise<Response> {
  try {
    return await fetch(url, init);
  } catch (err) {
    console.warn(`[runtime] ${label}: could not reach the platform:`, err instanceof Error ? err.message : err);
    throw new Error(`${label}: the platform could not be reached`);
  }
}

/**
 * sendSchedulerOps records ops through provisioning's
 * /internal/runtime/projects/{projectId}/scheduler route, in order and in
 * one transaction there (in provisioning's database). Auth is this project's runtime token.
 */
async function sendSchedulerOps(projectId: string, ops: SchedulerOp[]): Promise<void> {
  if (PROVISIONING_URL === "") {
    throw new Error("ctx.scheduler: EXCALIBASE_PROVISIONING_URL is not configured");
  }
  if (!projectId) {
    throw new Error("ctx.scheduler: projectId missing from invocation context");
  }
  const url = `${PROVISIONING_URL.replace(/\/$/, "")}/internal/runtime/projects/${encodeURIComponent(projectId)}/scheduler`;
  const res = await callProvisioning("ctx.scheduler", url, {
    method: "POST",
    headers: {
      "X-Excalibase-Runtime-Token": RUNTIME_SECRET,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ ops }),
  });
  if (!res.ok) {
    const text = await res.text();
    console.warn(`[runtime] ctx.scheduler: provisioning returned ${res.status}: ${text.slice(0, 200)}`);
    await res.body?.cancel();
    throw new Error(`ctx.scheduler: the platform rejected the request (status ${res.status})`);
  }
  await res.body?.cancel();
}

/**
 * flushSchedules sends the ctx.scheduler calls of a mutation that just
 * succeeded. It returns false when provisioning did not record them.
 */
async function flushSchedules(id: string, projectId: string, ops: SchedulerOp[]): Promise<boolean> {
  if (ops.length === 0) return true;
  try {
    await sendSchedulerOps(projectId, ops);
    return true;
  } catch (scheduleErr) {
    // The detail can name the internal provisioning endpoint, so it stays in
    // the server log; the caller gets only the fixed message below.
    const reason = scheduleErr instanceof Error ? scheduleErr.message : String(scheduleErr);
    console.warn(`[runtime] ${id} succeeded but its scheduled functions were not recorded:`, reason);
    return false;
  }
}

class FunctionRuntime {
  private readonly scripts = new Map<string, ScriptMetadata>();

  async deploy(req: DeployRequest): Promise<{ id: string; url: string }> {
    const { id, code, secrets = {} } = req;

    if (!id || !VALID_ID.test(id)) {
      throw new Error("Invalid function id");
    }
    const allowedHosts = parseAllowedHosts(req.allowedHosts);
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

    // The egress allowlist (EXC-348); `false` when there is nothing to grant.
    const netPermission: boolean | string[] = egressAllowlist(allowedHosts);

    // Phase 9b.G — grant the worker scoped `read` access to the vendored
    // @excalibase/server library directory ONLY. Without this Deno's import
    // map resolution succeeds (the key matches) but the worker is then
    // denied at the file:// load step ("Requires read access to …"). The
    // permission is a path allow-list, not a blanket `true` — user code
    // cannot read /etc/passwd, the host pgdata, or any other path.
    const readPermission: boolean | string[] = [VENDORED_LIB_DIR];

    const worker = new Worker(URL.createObjectURL(blob), {
      type: "module",
      // deno-lint-ignore no-explicit-any
      deno: {
        permissions: {
          net: netPermission,
          env: false,
          read: readPermission,
          write: false,
          run: false,
          ffi: false,
        },
      },
    } as any);

    // Capture metadata reported during init. The worker emits it before
    // 'ready'; we buffer here and forward to provisioning after the
    // handshake so a failed HTTP callback never blocks the deploy.
    let initMetadataExports: unknown = null;

    // Wait for the worker's 'ready' message. Installs a temporary handler
    // that swaps to the permanent router on first 'ready'.
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => {
        // Terminate the orphan so we don't leak it on init failure.
        try { worker.terminate(); } catch (terminateErr) { console.debug("[runtime] terminate on timeout:", terminateErr); }
        reject(new Error("worker init timeout"));
      }, WORKER_INIT_TIMEOUT_MS);
      worker.onmessage = (e) => {
        if (e.data?.type === "metadata") {
          initMetadataExports = e.data.exports;
          return;
        }
        if (e.data?.type === "bootError") {
          // Phase 9b.F — the user module failed to load (parse error,
          // unresolvable npm specifier, etc.). The worker has already
          // posted a structured message and re-thrown; the onerror
          // handler below will fire next. We pre-empt with the structured
          // message so the caller sees a deploy error that names the
          // root cause instead of a generic "worker init error".
          clearTimeout(timeout);
          try { worker.terminate(); } catch (terminateErr) { console.debug("[runtime] terminate on bootError:", terminateErr); }
          reject(new Error(typeof e.data.error === "string" && e.data.error.length > 0
            ? e.data.error
            : "user module failed to load"));
          return;
        }
        if (e.data?.type === "ready") {
          clearTimeout(timeout);
          resolve();
        }
      };
      worker.onerror = (err) => {
        clearTimeout(timeout);
        try { worker.terminate(); } catch (terminateErr) { console.debug("[runtime] terminate on error:", terminateErr); }
        // Phase 9b.F — module-eval failure inside a Deno worker fires
        // BOTH a structured bootError postMessage and the onerror event.
        // Use preventDefault() so the error is not re-thrown on the
        // parent process side (which would crash the runtime).
        try { err.preventDefault(); } catch (_e) { /* not all envs expose it */ }
        reject(new Error(err.message ?? "worker init error"));
      };
    });

    // Fire-and-forget forward — provisioning callback failures must never
    // affect deploy success. The Go side persists the payload on receipt.
    if (initMetadataExports != null && PROVISIONING_URL !== "") {
      forwardMetadataToProvisioning(id, initMetadataExports).catch((err) => {
        console.warn(`[runtime] metadata forward failed for ${id}:`, err);
      });
    }

    // Pull the kind off the metadata array (Phase 7 emits one entry per
    // worker — name=default, kind=<query|mutation|action|httpAction|
    // httpRouter>). Defaults to empty for legacy v1 fetch handlers.
    let detectedKind = "";
    if (Array.isArray(initMetadataExports) && initMetadataExports.length > 0) {
      const first = initMetadataExports[0] as { kind?: unknown };
      if (typeof first.kind === "string") detectedKind = first.kind;
    }
    const meta: ScriptMetadata = {
      id,
      worker,
      createdAt: new Date(),
      invocations: 0,
      pending: new Map(),
      nextReqId: 1,
      logs: [],
      kind: detectedKind,
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

      if (msg.type === "metadata") {
        // Late metadata (post-init). Forward as fire-and-forget — same
        // best-effort semantics as the init-time path.
        if (PROVISIONING_URL !== "") {
          forwardMetadataToProvisioning(id, msg.exports).catch((err) => {
            console.warn(`[runtime] late metadata forward failed for ${id}:`, err);
          });
        }
        return;
      }

      if (msg.type === "runX") {
        // Phase 7: ctx.runQuery/runMutation/runAction RPC from the worker.
        // Resolve the target fn against the runtime's local script table
        // (in-process composition — same project, sibling fn) and forward
        // the args through the regular invoke pipeline so validation,
        // ctx wiring, and error semantics match a direct call.
        //
        // Cross-runtime composition (target in a different deno-runtime
        // pod) falls back to an HTTP self-call to the gateway's
        // /internal/invoke route. Out of scope for the in-process tests;
        // the gateway side already supports the route.
        const rpcId = msg.rpcId;
        if (typeof rpcId !== "number") return;
        void (async () => {
          let result: { ok: true; data: unknown } | RunXFailure;
          try {
            const data = await dispatchRunX(meta.id, msg as RunXMessage);
            result = { ok: true, data };
          } catch (err) {
            result = runXFailure(err);
          }
          try {
            worker.postMessage({ type: "runXResult", rpcId, result });
          } catch (postErr) {
            console.debug("[runtime] runXResult postMessage failed:", postErr);
          }
        })();
        return;
      }

      if (msg.type === "scheduler") {
        // Phase 8: ctx.scheduler.{runAfter,runAt,cancel}.
        //
        // A mutation's calls wait for its commit; an action's are sent to
        // provisioning at once and stand on their own — Convex parity.
        const rpcId = msg.rpcId;
        if (typeof rpcId !== "number") return;
        void (async () => {
          let result: { ok: true; data: unknown } | { ok: false; error: string };
          try {
            const data = await dispatchScheduler(meta.id, msg, msg.txnRefId as string | undefined);
            result = { ok: true, data };
          } catch (err) {
            result = {
              ok: false,
              error: String((err instanceof Error ? err.message : err) ?? "scheduler error"),
            };
          }
          try {
            worker.postMessage({ type: "schedulerResult", rpcId, result });
          } catch (postErr) {
            console.debug("[runtime] schedulerResult postMessage failed:", postErr);
          }
        })();
        return;
      }

      if (msg.type === "storage") {
        // Phase 10: ctx.storage RPC. Translate the op into an HTTP call
        // against provisioning's /internal/storage/{projectId}/* routes
        // (runtime-token auth) and post the result back to the worker.
        // The runtime id is `${projectId}__${fnId}` (Phase 7 convention);
        // we split here so dispatchStorage knows which tenant to target.
        const rpcId = msg.rpcId;
        if (typeof rpcId !== "number") return;
        const sep = meta.id.indexOf("__");
        const projectId = sep > 0 ? meta.id.slice(0, sep) : "";
        void (async () => {
          let result: { ok: true; data: unknown } | { ok: false; error: string };
          try {
            const data = await dispatchStorage(projectId, msg);
            result = { ok: true, data };
          } catch (err) {
            result = {
              ok: false,
              error: String((err instanceof Error ? err.message : err) ?? "storage error"),
            };
          }
          try {
            worker.postMessage({ type: "storageResult", rpcId, result });
          } catch (postErr) {
            console.debug("[runtime] storageResult postMessage failed:", postErr);
          }
        })();
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
          ...(msg.failed === true ? { failed: true } : {}),
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
    return await this.invokeOnce(script, req, extractInheritedTxnRefId(req));
  }

  /**
   * Run the worker once. A top-level mutation opens a scope that collects
   * its ctx.scheduler calls; a nested mutation joins its caller's scope.
   */
  private async invokeOnce(
    script: ScriptMetadata,
    req: InvokeRequest,
    inheritedTxnRefId: string,
  ): Promise<InvokeResponse> {
    const id = script.id;
    script.invocations++;
    const reqId = script.nextReqId++;

    // The script id is `${projectId}__${fnId}`; v1 fetch handlers don't
    // follow this shape but they open no scope either.
    const sep = id.indexOf("__");
    const projectId = sep > 0 ? id.slice(0, sep) : "";

    const activeTxnRefId = mutationScopes.open(script.kind, inheritedTxnRefId, projectId);

    // Set when a mutation succeeded but provisioning refused or missed the
    // ctx.scheduler calls it made; the caller gets an error, not a success
    // that scheduled nothing.
    let scheduleFailed = false;
    // finalize closes the scope: a successful top-level mutation sends its
    // schedules, a failed one drops them. Either way the entry is removed
    // so a redeployed worker can't see stale refs.
    const finalize = async (settled: "success" | "error"): Promise<void> => {
      const closed = mutationScopes.close(activeTxnRefId, settled);
      if (closed.lost) {
        scheduleFailed = true;
        console.warn(`[runtime] ${id} succeeded after its calling mutation ended; its scheduled functions were dropped`);
        return;
      }
      scheduleFailed = !(await flushSchedules(id, projectId, closed.send));
    };

    let res: InvokeResponse;
    try {
      res = await new Promise<InvokeResponse>((resolve, reject) => {
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
        script.worker.postMessage({ type: "invoke", reqId, data: req, txnRefId: activeTxnRefId });
      });
    } catch (err) {
      await finalize("error");
      throw err;
    }

    // A v2 handler that threw is reported as a 200 envelope from the worker
    // with `{error: ...}` in the body (see __dispatchV2's catch branches).
    // Treat any non-2xx status, or any 200 whose body has a top-level
    // `error` field, as a failure that drops the scope's schedules.
    let isError = res.status < 200 || res.status >= 300;
    if (!isError && res.body) {
      try {
        const parsed = JSON.parse(res.body) as { error?: unknown };
        if (parsed && typeof parsed.error === "string") isError = true;
      } catch (_) { /* non-JSON body — treat as success */ }
    }
    await finalize(isError ? "error" : "success");
    if (scheduleFailed) {
      return {
        ...res,
        status: 500,
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          error: "the mutation succeeded but its scheduled functions were not recorded",
        }),
      };
    }
    return res;
  }

  delete(id: string): boolean {
    const script = this.scripts.get(id);
    if (!script) return false;
    script.worker.terminate();
    this.scripts.delete(id);
    return true;
  }

  /**
   * Returns the recorded export kind for a deployed function id, or the
   * empty string when the function is unknown or its metadata never
   * arrived. Phase 8 read-only enforcement consults this to validate
   * ctx.runX targets against the caller's kind.
   */
  getKind(id: string): string {
    return this.scripts.get(id)?.kind ?? "";
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

/**
 * Parse the caller's mutation-scope ref out of a v2 request body.
 * The body is JSON `{args, txnRefId?, runDepth?}`. Anything else (non-JSON,
 * missing field) means the caller is top-level.
 */
function extractInheritedTxnRefId(req: InvokeRequest): string {
  if (typeof req.body !== "string" || req.body.length === 0) return "";
  try {
    const parsed = JSON.parse(req.body) as { txnRefId?: unknown };
    if (typeof parsed.txnRefId === "string" && parsed.txnRefId.length > 0) {
      return parsed.txnRefId;
    }
  } catch (_) { /* not JSON — top-level path */ }
  return "";
}

const runtime = new FunctionRuntime();

// Reactive subscriptions live in graphql now (Phase 15a). This runtime is
// pure RPC: HTTP /invoke + the Phase 15b `{result, reads}` envelope is
// the only reactive surface still owned here, and graphql opts in via
// the `X-Excalibase-Envelope: v1` header. No in-process subscription
// registry, no NATS bridge, no sibling WS listener.

/**
 * Best-effort callback: POST captured v2 export metadata back to the Go
 * provisioning service. The runtime id is `${projectId}__${fnId}`; the Go
 * route is `/internal/runtime/functions/{fnId}/metadata`, so we split.
 * Auth: X-Excalibase-Runtime-Token shared with the deploy/invoke RPCs.
 *
 * Failures are logged and swallowed — a deploy never blocks on metadata
 * capture, and Phase 3 codegen tolerates a missing exports array.
 */
async function forwardMetadataToProvisioning(runtimeID: string, exports: unknown): Promise<void> {
  if (PROVISIONING_URL === "") return;
  const sep = runtimeID.indexOf("__");
  if (sep < 0) {
    console.warn(`[runtime] metadata forward: runtime id ${runtimeID} missing __ separator`);
    return;
  }
  const projectId = runtimeID.slice(0, sep);
  const fnId = runtimeID.slice(sep + 2);
  const url = `${PROVISIONING_URL.replace(/\/$/, "")}/internal/runtime/functions/${fnId}/metadata`;
  const res = await fetch(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Excalibase-Runtime-Token": RUNTIME_SECRET,
    },
    body: JSON.stringify({ projectId, exports }),
  });
  // Drain the body so the connection can be reused.
  await res.body?.cancel();
  if (!res.ok) {
    console.warn(`[runtime] metadata forward HTTP ${res.status} for ${runtimeID}`);
  }
}

/**
 * Phase 10 — dispatch `ctx.storage` RPC ops over the provisioning
 * /internal/storage/{projectId}/* routes. The Deno runtime is the only
 * caller; auth uses the X-Excalibase-Runtime-Token shared secret, same
 * as /internal/invoke and /internal/runtime/functions/.../metadata.
 *
 * Op contract (matches the worker-side __makeStorageReader/Writer in the
 * worker template):
 *
 *   "generateUploadUrl" — POST /internal/storage/{p}/upload-url
 *      Payload: { contentType: string, size: number }  (both required: the
 *      signed PUT binds them, so a mismatch is refused by the object store)
 *      Returns: { url, storageId, uploadId }
 *
 *   "completeUpload"    — POST /internal/storage/{p}/confirm-upload
 *      Accepts a direct upload the client PUT to the staging URL. Without
 *      it the bytes are never recorded and the reaper collects them.
 *      Payload: { storageId, uploadId }
 *      Returns: string (the confirmed storageId)
 *
 *   "store"             — generates a signed URL via upload-url, PUTs the
 *      bytes carried in the payload, posts confirm-upload, returns the
 *      minted storageId. Mirrors the Convex client direct-upload flow
 *      but server-side.
 *      Payload: { bytes: Uint8Array, contentType: string, sha256?: string }
 *      Returns: string (storageId)
 *      The PUT must carry exactly the Content-Type and Content-Length the
 *      URL was signed for; anything else is a 403 from the object store.
 *
 *   "getUrl"            — POST /internal/storage/{p}/download-url
 *      Payload: { storageId }
 *      Returns: string | null  (null on 404)
 *
 *   "get"               — GET on the signed URL, returns the bytes.
 *      Payload: { storageId }
 *      Returns: { bytes: Uint8Array, contentType: string } | null
 *
 *   "getMetadata"       — GET /internal/storage/{p}/metadata/{storageId}
 *      Payload: { storageId }
 *      Returns: { storageId, sha256, size, contentType? } | null  (null on 404)
 *
 *   "delete"            — DELETE /internal/storage/{p}/{storageId}
 *      Payload: { storageId }
 *      Returns: null  (idempotent — 204 on success)
 */
async function dispatchStorage(
  projectId: string,
  msg: {
    op: string;
    payload?: {
      storageId?: string;
      uploadId?: string;
      bytes?: Uint8Array;
      contentType?: string;
      size?: number;
      sha256?: string;
    };
  },
): Promise<unknown> {
  if (PROVISIONING_URL === "") {
    throw new Error("ctx.storage: EXCALIBASE_PROVISIONING_URL is not configured");
  }
  if (!projectId) {
    throw new Error("ctx.storage: projectId missing from invocation context");
  }
  const base = `${PROVISIONING_URL.replace(/\/$/, "")}/internal/storage/${encodeURIComponent(projectId)}`;
  const tokenHeaders: Record<string, string> = {
    "X-Excalibase-Runtime-Token": RUNTIME_SECRET,
  };
  const jsonHeaders: Record<string, string> = {
    ...tokenHeaders,
    "Content-Type": "application/json",
  };
  const payload = msg.payload || {};
  const storageId = payload.storageId;

  switch (msg.op) {
    case "generateUploadUrl": {
      const contentType = payload.contentType || "";
      const size = payload.size || 0;
      if (contentType === "" || size <= 0) {
        throw new Error("generateUploadUrl: contentType and size are required");
      }
      const res = await callProvisioning("ctx.storage.generateUploadUrl", `${base}/upload-url`, {
        method: "POST",
        headers: jsonHeaders,
        body: JSON.stringify({ contentType, size }),
      });
      if (!res.ok) {
        const text = await res.text();
        throw new Error(`generateUploadUrl HTTP ${res.status}: ${text.slice(0, 200)}`);
      }
      return await res.json();
    }
    case "completeUpload": {
      const uploadId = payload.uploadId;
      if (typeof storageId !== "string" || storageId.length === 0) {
        throw new Error("completeUpload: storageId required");
      }
      if (typeof uploadId !== "string" || uploadId.length === 0) {
        throw new Error("completeUpload: uploadId required");
      }
      const res = await callProvisioning("ctx.storage.completeUpload", `${base}/confirm-upload`, {
        method: "POST",
        headers: jsonHeaders,
        body: JSON.stringify({ storageId, uploadId }),
      });
      if (!res.ok) {
        const text = await res.text();
        throw new Error(`completeUpload HTTP ${res.status}: ${text.slice(0, 200)}`);
      }
      await res.body?.cancel();
      return storageId;
    }
    case "getUrl": {
      const res = await callProvisioning("ctx.storage.getUrl", `${base}/download-url`, {
        method: "POST",
        headers: jsonHeaders,
        body: JSON.stringify({ storageId }),
      });
      if (res.status === 404) {
        await res.body?.cancel();
        return null;
      }
      if (!res.ok) {
        const text = await res.text();
        throw new Error(`getUrl HTTP ${res.status}: ${text.slice(0, 200)}`);
      }
      const body = await res.json() as { url: string };
      return body.url;
    }
    case "get": {
      // Two-step: ask provisioning for a signed GET URL, then fetch it.
      const signed = await dispatchStorage(projectId, { op: "getUrl", payload: { storageId } }) as string | null;
      if (signed === null) return null;
      const r = await callProvisioning("ctx.storage.get", signed);
      if (r.status === 404) {
        await r.body?.cancel();
        return null;
      }
      if (!r.ok) {
        const text = await r.text();
        throw new Error(`get HTTP ${r.status}: ${text.slice(0, 200)}`);
      }
      const ct = r.headers.get("content-type") || "application/octet-stream";
      const buf = new Uint8Array(await r.arrayBuffer());
      return { bytes: buf, contentType: ct };
    }
    case "getMetadata": {
      if (typeof storageId !== "string" || storageId.length === 0) {
        throw new Error("getMetadata: storageId required");
      }
      const res = await callProvisioning("ctx.storage.getMetadata", `${base}/metadata/${encodeURIComponent(storageId)}`, {
        headers: tokenHeaders,
      });
      if (res.status === 404) {
        await res.body?.cancel();
        return null;
      }
      if (!res.ok) {
        const text = await res.text();
        throw new Error(`getMetadata HTTP ${res.status}: ${text.slice(0, 200)}`);
      }
      return await res.json();
    }
    case "store": {
      const bytes = payload.bytes;
      const contentType = payload.contentType || "application/octet-stream";
      if (!bytes || !(bytes instanceof Uint8Array)) {
        throw new Error("store: payload.bytes (Uint8Array) required");
      }
      // 1. Mint upload-url + storageId.
      const mint = await callProvisioning("ctx.storage.store", `${base}/upload-url`, {
        method: "POST",
        headers: jsonHeaders,
        body: JSON.stringify({ contentType, size: bytes.byteLength }),
      });
      if (!mint.ok) {
        const text = await mint.text();
        throw new Error(`store/upload-url HTTP ${mint.status}: ${text.slice(0, 200)}`);
      }
      const mintBody = await mint.json() as { url: string; storageId: string; uploadId: string };
      if (!mintBody.uploadId) {
        throw new Error("store/upload-url: reply carried no uploadId");
      }
      // 2. PUT the bytes to the signed URL. The signature covers both
      //    headers, so they must be exactly what was declared at mint time —
      //    the same type, and the same number of bytes.
      const put = await callProvisioning("ctx.storage.store", mintBody.url, {
        method: "PUT",
        headers: {
          "Content-Type": contentType,
          "Content-Length": String(bytes.byteLength),
        },
        body: bytes as BodyInit,
      });
      if (!put.ok) {
        await put.body?.cancel();
        throw new Error(`store/PUT HTTP ${put.status}`);
      }
      await put.body?.cancel();
      // 3. Confirm.
      const confirm = await callProvisioning("ctx.storage.store", `${base}/confirm-upload`, {
        method: "POST",
        headers: jsonHeaders,
        body: JSON.stringify({
          storageId: mintBody.storageId,
          uploadId: mintBody.uploadId,
          sha256: payload.sha256 || "",
        }),
      });
      if (!confirm.ok) {
        const text = await confirm.text();
        throw new Error(`store/confirm-upload HTTP ${confirm.status}: ${text.slice(0, 200)}`);
      }
      await confirm.body?.cancel();
      return mintBody.storageId;
    }
    case "delete": {
      if (typeof storageId !== "string" || storageId.length === 0) {
        throw new Error("delete: storageId required");
      }
      const res = await fetch(`${base}/${encodeURIComponent(storageId)}`, {
        method: "DELETE",
        headers: tokenHeaders,
      });
      await res.body?.cancel();
      if (!res.ok && res.status !== 404) {
        throw new Error(`delete HTTP ${res.status}`);
      }
      return null;
    }
    default:
      throw new Error(`ctx.storage: unsupported op ${msg.op}`);
  }
}

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

// BOOT_ID is minted once per process. Deployed functions live in memory only,
// so provisioning watches this value on /health: a change means this runtime
// restarted and every function must be replayed from the store (EXC-337).
const BOOT_ID = crypto.randomUUID();

// draining is set once the runtime has been told to stop: /health fails so
// readiness takes the pod out of the Service while it still serves.
let draining = false;

function handleHealth(): Response {
  if (draining) {
    return Response.json({ status: "draining", bootId: BOOT_ID }, { status: 503, headers: JSON_HEADERS });
  }
  return Response.json(
    {
      status: "healthy",
      scripts: runtime.stats().totalScripts,
      uptime: performance.now(),
      bootId: BOOT_ID,
    },
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
  try {
    const result = await runtime.deploy(parsed);
    return Response.json(result, { status: 201, headers: JSON_HEADERS });
  } catch (error: unknown) {
    if (error instanceof AllowedHostsError) return badRequest(error.message);
    throw error;
  }
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
  const result = withoutCrashDetail(await runtime.invoke(id, invokeReq));
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

const server = Deno.serve({ port: PORT }, async (req: Request) => {
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

// An image whose --allow-env predates the setting gets the default drain.
function readDrainEnv(): string | undefined {
  try {
    return Deno.env.get("EXCALIBASE_SHUTDOWN_DRAIN_MS");
  } catch (_) {
    return undefined;
  }
}

// On SIGTERM/SIGINT keep serving while the Service forgets this pod, then
// stop the listener (EXC-569).
const shutdown = drainThenExit({
  drainMs: shutdownDrainMs(readDrainEnv()),
  markDraining: () => { draining = true; },
  sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  stopServing: () => server.shutdown(),
  exit: (code) => Deno.exit(code),
});
try { Deno.addSignalListener("SIGTERM", shutdown); } catch (_) { /* not all platforms */ }
try { Deno.addSignalListener("SIGINT", shutdown); } catch (_) { /* not all platforms */ }

console.log(`Excalibase Deno runtime on :${PORT}${V2_ENABLED ? " (functions v2 enabled)" : ""}`);
