# Excalibase Deno Edge Function Runtime

Self-hosted serverless platform — runs Supabase-style multi-file Deno functions
in isolated Web Workers. Compatible with the `(req: Request) => Response`
handler shape.

## Architecture

The Excalibase platform manages this runtime — users typically don't talk to
it directly. The platform deploys functions, sets secrets in vault, and proxies
invocations through `/functions/v1/{projectId}/{name}` on the platform side.

```
Studio UI
  ↓ POST /api/projects/{pid}/functions
Platform (Go)
  ↓ POST /deploy { id, code, secrets }   (X-Runtime-Secret header)
Deno runtime (this server)
  ↓ spawn Web Worker per function
  ↓ inject secrets via Deno.env mock
  ↓ user code runs
```

Two deployment modes:

1. **Per-project** (production) — the platform calls `EnsureDenoRuntime` to
   create one Deno pod per project namespace. Each pod hosts only that
   project's functions. **This is the default.** See
   `internal/k8s/client.go:EnsureDenoRuntime` for the generated manifest.
2. **Shared fallback** (dev / single-tenant) — `k8s/deployment-shared.yaml`
   creates one Deno pod in the `serverless` namespace shared across all
   projects, with function ids prefixed by `{projectId}__` to avoid
   collisions. Use this when running locally without K8s API access.

## Build

The Dockerfile is **multi-stage** (Phase 9b.G): stage 1 builds the sibling
`@excalibase/server` library so the resulting image is self-contained and
needs no host mount. Set the build context to the **parent** directory
that has both `excalibase-provisioning/` and `excalibase-server/` as
siblings:

```bash
# From the monorepo root that contains both repos:
docker build -t excalibase/deno-runtime:latest \
  -f excalibase-provisioning/deno-server/Dockerfile \
  .
```

When the build context lacks `excalibase-server/`, the stage 1 `COPY`
errors loudly — preferable to silently shipping an image that 404s every
function deploy.

### Vendoring `@excalibase/server` (Phase 9b.G)

User function bundles import `npm:@excalibase/server@X.Y.Z` literally; the
package is **workspace-internal** and never published to npm. The runtime
resolves the import via Deno's import map (`deno.json` at `/app/`):

```jsonc
{
  "imports": {
    "npm:@excalibase/server@0.10.0": "./vendor/excalibase-server/index.mjs",
    "zod-to-json-schema": "npm:zod-to-json-schema@^3.22.0",
    "zod": "npm:zod@^3.22.0"
  }
}
```

**Wildcard caveat:** Deno 2.7's import map does NOT honour
`npm:@excalibase/server@*` — each supported version must be pinned
explicitly. When the lib version bumps, add the new entry to `deno.json`
or the deploy fails fast with `npm package … does not exist`.

**Local dev / e2e:** the host symlinks `vendor/excalibase-server/index.mjs`
to the sibling `../../../excalibase-server/dist/index.mjs` (auto-created
by `make e2e-reactive` in the graphql repo). The test harness reads from
the same path so `deno test` and the container converge on the same file
layout.

**Multi-version support:** the import map can map MULTIPLE version keys
to the SAME on-disk file. This means a single runtime image happily
serves bundles pinned to 0.4.0, 0.7.0, AND 0.10.0 simultaneously — as
long as the dist on disk is API-compatible with every pinned version.
When a breaking change lands in the lib, ship a NEW runtime image with
multiple dists side by side and remap the version keys accordingly.

## Deploy

### Per-project (production)

You don't deploy this manually. The platform's `EnsureDenoRuntime` k8s helper
creates a Deployment + Service inside each project's namespace on the first
function deploy. Image is pulled from the registry pointed at by the
`DENO_RUNTIME_IMAGE` config var on the platform.

### Shared fallback (dev)

```bash
# Load image into minikube
minikube image load excalibase/deno-runtime:latest

# Deploy the shared pod
kubectl apply -f k8s/deployment-shared.yaml

# Verify
kubectl get pods -n serverless
```

## Protocol

All requests except `/health` require the `X-Runtime-Secret` header. Constant-
time comparison.

Deployed functions live in memory only. `bootId` on `/health` is a random id
minted once per process; provisioning polls it and replays the project's
functions from its store when the id changes (see
[docs/functions-runtime-replay.md](../docs/functions-runtime-replay.md)).

| Method | Endpoint | Body | Returns |
|--------|----------|------|---------|
| GET | `/health` | — | `{ status, scripts, uptime, bootId }` |
| POST | `/deploy` | `DeployRequest` | `{ id, url }` |
| POST | `/invoke/{id}` | `InvokeRequest` | `InvokeResponse` |
| DELETE | `/delete/{id}` | — | `{ status, id }` |
| GET | `/scripts` | — | `{ scripts: [...] }` |
| GET | `/stats` | — | `{ totalScripts, maxScripts, scripts }` |

### `DeployRequest`

```ts
{
  id: string;                          // 1-128 chars, [a-zA-Z0-9_-]
  code: string;                        // bundled JS module source (≤512 KB)
  secrets?: Record<string, string>;    // injected as Deno.env at runtime
}
```

The `code` field is the **bundled** module produced by the Go side
(`internal/edgefn/function.go:Bundle()`). It must define a default handler at
`globalThis.__excalibase_default`. The platform's bundler rewrites
`export default X` into that assignment automatically — users just write
normal Deno code.

### `InvokeRequest` / `InvokeResponse`

```ts
// Request
{
  method: string;                       // GET, POST, ...
  url: string;                          // forwarded to the user's Request object
  headers: Record<string, string>;
  body: string;                         // already string; runtime won't decode
}

// Response
{
  status: number;                       // from user's Response
  headers: Record<string, string>;
  body: string;
}
```

### What the user writes

```ts
// index.ts — uploaded to the platform
export default async (req: Request): Promise<Response> => {
  const { name = "world" } = await req.json().catch(() => ({}));
  const stripeKey = Deno.env.get("STRIPE_KEY");
  return Response.json({ message: `Hello ${name}`, hasKey: !!stripeKey });
};
```

Multi-file is supported via the platform — additional files go alongside
`index.ts` (e.g. `utils.ts`, `_shared/cors.ts`). The platform inlines them
before sending to the runtime; the runtime sees a single bundled `code`
string.

## Test

```bash
# Port-forward (shared fallback mode)
kubectl port-forward -n serverless svc/deno-runtime 24006:8000 &

# Health (no auth)
curl http://localhost:24006/health

# Deploy
curl -X POST http://localhost:24006/deploy \
  -H "X-Runtime-Secret: $RUNTIME_SECRET" \
  -H "Content-Type: application/json" \
  -d '{
        "id": "hello",
        "code": "globalThis.__excalibase_default = (req) => Response.json({ msg: \"hi\" });",
        "secrets": {}
      }'

# Invoke
curl -X POST http://localhost:24006/invoke/hello \
  -H "X-Runtime-Secret: $RUNTIME_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"method":"POST","url":"http://fn/","headers":{},"body":"{}"}'
# → {"status":200,"headers":{"content-type":"application/json"},"body":"{\"msg\":\"hi\"}"}
```

## Limits & isolation

| | Default | Notes |
|---|---|---|
| Max scripts per pod | 100 | Exceeding throws on `/deploy` |
| Max code size | 512 KB | Bundled — uncompressed |
| Max invoke body | 1 MB | Refused with 413 via `Content-Length` before reading |
| Invoke timeout | 30 s | Worker killed on timeout |
| Worker init timeout | 5 s | Orphan worker is `terminate()`'d |
| Concurrent invokes | unlimited per worker | Correlated by request id, no race |

### Worker permissions (per-script V8 isolate)

| Permission | Default |
|---|---|
| `net` | `false`, or allowlist via `ALLOWED_HOSTS` env |
| `read` | scoped to `EXCALIBASE_VENDORED_LIB_DIR` only (default `/app/vendor/excalibase-server`) — the vendored `@excalibase/server` library lives there and the worker must load it via the import map. User code cannot read `/etc/passwd` or any other path. |
| `write` | `false` |
| `env` | `false` (real env hidden — only the per-function `Deno.env` mock is exposed) |
| `run` | `false` |
| `ffi` | `false` |

Secrets are injected via a mock `Deno.env` shadow installed inside an IIFE
that wraps user code. The real `Deno.env` is unreachable.

## Security model

- **Auth**: `X-Runtime-Secret` shared between platform and runtime, via env var
  (k8s Secret in production). Constant-time comparison, no timing oracle.
- **Network isolation**: in production each project has its own runtime pod
  inside its own namespace. NetworkPolicy can be added to restrict ingress to
  the platform pod and egress to the project's CNPG postgres only.
- **Isolation**: each function runs in its own Deno Web Worker = separate V8
  isolate, separate heap. A misbehaving function can't read another's memory.
- **Resource caps**: per-pod CPU/memory limits are enforced by k8s. There is
  no per-function CPU cap inside the worker — a tight loop will be terminated
  by the 30s invoke timeout.
- **No filesystem access**: `read: false, write: false` permissions block
  reading source/secrets from disk even if a function tries.
