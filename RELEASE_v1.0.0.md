# v1.0.0 release plan

5-repo lockstep tag. Each repo gets a **single** `v1.0.0` tag pinned to the same
service contract (vault paths, JWT claims, schema URLs, capacity API).

## Repos in lockstep

| # | Repo | Image | Tag |
|---|---|---|---|
| 1 | `excalibase-provisioning` (server-go) | `excalibase/provisioning:v1.0.0` | `v1.0.0` |
| 2 | `excalibase-provisioning` (deno-server) | `excalibase/deno-runtime:v1.0.0` | shares the repo's tag |
| 3 | `excalibase-auth` | `excalibase/auth:v1.0.0` | `v1.0.0` |
| 4 | `excalibase-graphql` | `excalibase/excalibase-graphql:v1.0.0` | `v1.0.0` |
| 5 | `excalibase-watcher-go` | `excalibase/excalibase-watcher-go:v1.0.0` | `v1.0.0` |
| 6 | `excalibase-service` (Helm chart) | n/a (chart only) | `v1.0.0` |

## Pre-tag checklist

- [ ] Snyk container scan green / triaged on all 5 images (no critical/high un-triaged)
- [ ] AIO E2E self-test 72/0/1 (only F19 SSE-skip allowed) on a fresh cluster
- [ ] Frontend Vitest 100% (118/118)
- [ ] Frontend Playwright pass on `REAL_E2E=1`
- [ ] `OPERATOR.md` reviewed
- [ ] `values-prod.yaml` reviewed
- [ ] Bootstrap admin password rotation documented
- [ ] CHANGELOG entries written in each repo

## Tag order (build images first, tag last)

Doing it in this order means a partial release can't leave consumers pulling an image that was never tagged.

1. **Build all 5 images** locally with `:v1.0.0` tags. Push to the registry.
2. **Tag in dependency order** (least-dependent first):
   1. `excalibase-watcher-go` — no peers depend on its repo state at tag time
   2. `excalibase-provisioning/deno-server` — same
   3. `excalibase-auth` — emits JWTs that graphql + provisioning verify
   4. `excalibase-graphql` — consumes auth JWTs + provisioning vault
   5. `excalibase-provisioning/server-go` — orchestrates everything; tag last so its release notes can list the others
   6. `excalibase-service` chart — bumps all 5 image tags to `:v1.0.0`, tag the chart `v1.0.0`
3. **Smoke test the AIO chart** with the new tags (`helm upgrade --install platform … -f values-prod.yaml`).

## Tag commands per repo

```bash
# In each repo, after the release commit:
git tag -a v1.0.0 -m "Excalibase v1.0.0"
git push origin v1.0.0
```

## Image build/push per repo

```bash
# Provisioning (server-go)
docker build -t excalibase/provisioning:v1.0.0 server-go/
docker tag  excalibase/provisioning:v1.0.0 excalibase/provisioning:latest
docker push excalibase/provisioning:v1.0.0
docker push excalibase/provisioning:latest

# Deno runtime
docker build -t excalibase/deno-runtime:v1.0.0 deno-server/
docker push excalibase/deno-runtime:v1.0.0
docker push excalibase/deno-runtime:latest

# Auth
cd ../excalibase-auth && make docker.build
docker tag  excalibase-auth:latest excalibase/auth:v1.0.0
docker push excalibase/auth:v1.0.0
docker push excalibase/auth:latest

# GraphQL (Maven, slowest)
cd ../excalibase-graphql && make build-image
docker tag  excalibase/excalibase-graphql:latest excalibase/excalibase-graphql:v1.0.0
docker push excalibase/excalibase-graphql:v1.0.0
docker push excalibase/excalibase-graphql:latest

# Watcher
cd ../excalibase-watcher-go && docker build -t excalibase/excalibase-watcher-go:v1.0.0 .
docker tag  excalibase/excalibase-watcher-go:v1.0.0 excalibase/excalibase-watcher-go:latest
docker push excalibase/excalibase-watcher-go:v1.0.0
docker push excalibase/excalibase-watcher-go:latest
```

## What's in v1.0.0

### New
- Per-project edge functions (Deno) with JWT scope forwarding
- Per-project realtime CDC publication management
- Cluster capacity pre-flight + `GET /api/capacity` with per-tier project headroom
- Platform admin page: cross-org project listing, force-drop, org revocation, audited
- `GET /api/admin/logs` Loki proxy across services + per-tenant filtering
- BYOC SSRF guard, `RequireProjectAccess` middleware (IDOR fix)
- Cookie auth + scopes + token expiry + revocation
- Cross-project JWT replay rejection (projectId binding)
- Vault project-scoped path scheme (no orgSlug guessing)
- Standalone CNPG instance PodMonitor (CNPG operator's own podmonitor crashes)

### Fixed (v0.x → v1.0.0 breaking)
- Schema routes: `/{orgId}/{projectId}` → `/{projectId}` (orgId was dead weight)
- Watcher uses `cdc_watcher` role from vault, not CNPG `app` secret (race fixed)
- Auth password-flow JWT now mints `scope: "authenticated"` claim
- All previously aggregate `lookup` rate limits split into PerIP / PerUser / PerProjectAndUser
- Vault prefix purge on deprovision (was leaking creds for BYOC)

### Deferred to v1.1
- Demo project (needs admin UI for management which v1 doesn't have at full breadth)
- LogQL passthrough for power-user queries (v1 has fixed-schema service= filter)
- Live tail (SSE) on /api/admin/logs (v1 returns snapshots)
- Per-project Loki tenant labels for log isolation
- Native Prometheus client in auth + provisioning (Go services log-derived metrics today)

## Post-tag

- Update `excalibase-service` chart `appVersion: 1.0.0` and `version: 1.0.0`
- Publish chart to chart registry (if used)
- Cut release notes from this file on each repo's GitHub Releases
- Update `CLAUDE.md` files to point at `v1.0.0` examples
