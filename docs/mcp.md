# MCP for coding tools

Excalibase serves an MCP endpoint (streamable HTTP) at `https://<studio host>/mcp`
(`https://app.excalibase.io/mcp` on the hosted platform). AI coding tools use it to
read the schema, run SQL and migrations, generate types, set API permissions, deploy
functions and apps, and read logs (ADR 0037, decision 6).

MCP ships dark (EXC-554): until an install sets `FEATURE_MCP=true` (chart value
`features.mcp`), `/mcp`, `GET /api/schema/{projectId}/query` and the AI activity
feed answer 404 and Studio hides AI Tools. The deploy-by-image and CI tools also
need `FEATURE_PIPELINE=true` (`features.pipeline`).

## Authentication

Send a personal access token in `Authorization: Bearer <token>`. Sign-in sessions,
cookies and service tokens are refused. Studio's "Connect your AI tool" page mints a
token bound to one project.

Two query parameters can only narrow what the token allows:

| Parameter | Effect |
|---|---|
| `read_only=true` | No write tool is offered; `execute_sql` runs one statement in a read-only transaction on a connection that is closed afterwards, and refuses functions that act outside it (`pg_terminate_backend`, `dblink*`, `lo_*`, `pg_notify`, ...). A token with only the `read` scope is always read-only. |
| `project=<id>` | Every tool acts on that project only. A token bound to another project is refused. |

A URL that asks for more than the token allows (another project, a malformed value) still
connects, but every tool call answers why it is refused and reaches nothing: some clients hide
a refused connection's reason from the user.

## How a tool is authorized

A tool is an in-process request through the same API routes Studio calls, carrying
the caller's token (narrowed as above). Org roles, project access, token scopes and
the route authorization table apply unchanged: a viewer cannot apply a migration
through MCP any more than through Studio. Every call is audited with the tool name,
the token, the project and the outcome; arguments (SQL, secrets) are not kept, and
read-only SQL is masked in the access log. A call whose arguments fail the tool's
input schema is refused before it runs and is not audited.

MCP never offers project creation or deletion, members and roles, tiers, sign-in
providers, backups and restores, credential reads or rotation, or token minting.
`create_publishable_key` makes only publishable SDK keys, which ship inside client
apps and grant what the anon role grants.

## Tools

| Tool | Route |
|---|---|
| `list_projects` | `GET /api/provision/` |
| `get_project_info` | `GET /api/projects/{id}/info/`, `GET /api/projects/{id}/sdk-keys/`; also returns `restApi`, the REST surface below |
| `create_publishable_key` | `POST /api/projects/{id}/sdk-keys/` (publishable only) |
| `list_tables` | `GET /api/schema/{id}/tables` |
| `describe_table` | `GET /api/schema/{id}/tables/{t}/columns`, `.../indexes`, `GET /api/schema/{id}/relationships` |
| `get_graphql_schema` | the tables, columns and foreign keys the API is generated from, plus the introspection command |
| `generate_typescript_types` | `GET /api/schema/{id}/tables` and each table's columns |
| `execute_sql` | `POST /api/schema/{id}/query`; read-only: `GET /api/schema/{id}/query?sql=` |
| `list_migrations` / `apply_migration` | `GET` / `POST /api/provision/{id}/migrations/` |
| `list_permissions` / `set_permission` | `GET /api/provision/{id}/permissions/`, `PUT`/`DELETE .../permissions/tables/{t}/roles/{r}/{op}`; counting rows needs `allowAggregations` |
| `list_functions` / `deploy_function` / `set_function_secret` | `GET`/`POST /api/projects/{id}/functions/`, `POST .../functions/secrets` |
| `list_apps` / `deploy_app` / `get_deploy_status` | `GET /api/projects/{id}/apps/` (+ `.../apps/{app}/deploys?limit=1` to report `NOT_DEPLOYED`); `POST .../apps/{app}/deploy` (a private-registry image needs its credential saved in Studio: a refusal names the registry and where to add it; where the pipeline flag is off, the image is stored with `PATCH` and the app deployed bodyless) with `{image, commitSha}` or empty to run the app's own image, pinned to its digest either way when the registry is public (the result's `unpinned` says when it is not); `GET .../apps/{app}/` + `.../deploys` or `.../deploys/{deployId}`; `state` is "rolling out" during a rollout and "succeeded" only once the app URL answers an HTTP probe (3 s), else "running, URL not answering yet" with the reason |
| `create_app` | `POST /api/projects/{id}/apps/` (Studio's plan limits apply), then `GET`/`PUT /api/projects/{id}/cors/` to allow the app's own origin; deleting the app removes that origin again |
| `update_app` / `delete_app` | `GET` then `PATCH /api/projects/{id}/apps/{app}/` at the version read (`If-Match`; env is merged by name, secrets kept as stored); `PUT /api/projects/{id}/app-network/` for the private-network switch (admin); `DELETE .../apps/{app}/` removes only that app and its CORS origin; an app with a disk is refused |
| `test_auth_flow` | `GET /api/projects/{id}/info/`, then one register or password sign-in against the project's own auth API with a test account the caller gives; reports status, outcome and the userId and role claims, never the password or a token; with `origin` the CORS preflight is checked first |
| `get_logs` | database: `GET /api/provision/{id}/logs`; app: `.../apps/{a}/logs`; function: `.../functions/{f}/logs`. A log backend that does not answer gives "not available", never its address |
| `get_slow_queries` | `GET /api/provision/{id}/slow-queries/?sort=total\|mean&limit=N`: the project's own normalized statements; where `FEATURE_SLOWQUERIES` is off the route answers 404. Reset and turning statistics on stay in Studio |
| `test_api_request` | `GET /api/projects/{id}/info/`, then one request to the project's own data API (below) |
| `get_dockerfile_template` | none: Dockerfiles for node, nextjs, vite, python, go, java |
| `list_sites` / `create_site` / `delete_site` | `GET`/`POST /api/projects/{id}/sites/`, `DELETE .../sites/{site}/`. Sites serve built files from the CDN (ADR 0043); the site's origin joins the project's CORS list on its first deploy |
| `deploy_site` | `POST .../sites/{site}/deploy?label=` with the archive as the raw body, then `GET .../sites/{site}/` for its URL. The tool takes the build output as a base64 gzipped tar (`tar czf - -C dist . \| base64`), at most 1 MiB before encoding so it fits one MCP message; the route and the sites service check it exactly as an upload from CI. A bigger build goes up from CI or a shell (`get_ci_snippet` with `kind: site`) |
| `list_site_deploys` / `rollback_site` | `GET .../sites/{site}/deploys`; `POST .../sites/{site}/deploys/{deploy}/rollback` |
| `list_site_domains` / `add_site_domain` / `check_site_domain` / `remove_site_domain` | `GET`/`POST .../sites/{site}/domains`, `POST .../sites/{site}/domains/{domain}/refresh`, `DELETE .../sites/{site}/domains/{domain}`. Adding only claims the name and answers the DNS records to add: a CNAME to the zone's target and a TXT record at `_excalibase-challenge.<host>` proving control (plus Cloudflare's TXT records with `txt` validation). The first check that sees the TXT record creates the domain on the user-content zone through Cloudflare for SaaS; it serves once Cloudflare reports it and its certificate active. Serving domains are checked again hourly and taken off when the TXT record, the CNAME or the certificate is gone. Adds and removes are limited to 6 an hour per project, and the zone holds at most `SITES_MAX_ZONE_HOSTNAMES` (default 90). The zone itself (fallback origin, routes) is never reachable through MCP |
| `connect_site_repo` / `list_site_builds` | `PUT .../sites/{site}/git` (left without `build_env`, the current build variables are read with `GET .../sites/{site}/git` and kept); refused for want of a GitHub sign-in, it tells the user to sign in from the site in Studio (the sign-in is bound to the browser that starts it, so MCP never starts one). `GET .../sites/{site}/builds`, or `.../builds/{build}` with the log (Developer and above). Git builds (EXC-354) are behind `SITES_GIT_BUILDS_ENABLED`; off, both answer not available and point to `deploy_site` or CI; single-host answers why |
| `get_ci_snippet` | `kind: app` (default): `GET .../apps/{app}/` for the app's image; renders the same GitHub Actions, GitLab CI, Jenkins or curl pipeline as Studio's pipeline page. `kind: site`: `GET .../sites/{site}/` when a site is named; the pipeline runs `npm ci` and `npm run build` in `context`, packs `output` (default `dist`) with `tar czf` and uploads it with one curl using the `EXCALIBASE_DEPLOY_TOKEN` CI secret: the site's deploy token, made by a Developer in Studio under the site's Deploy tokens and able to deploy only that site (EXC-620). MCP's own `deploy_site` keeps using the caller's personal access token |

Site writes need Developer or above, as on the HTTP routes. Where Sites is off (no Cloudflare
account configured, the default for Docker/Podman and self-hosted Kubernetes) every site tool
answers that Sites is not available on this install and points to a container
(`get_dockerfile_template`, `create_app`, `deploy_app`). Routing settings (`PATCH .../sites/{site}/`)
stay in Studio.

## The REST surface a page uses

`get_project_info` returns this as `restApi`, so a client does not need the engine's source.

- Table URL: `{base}/{projectId}/api/v1/{table}`, with `Authorization: Bearer <accessToken>` from the publishable-key exchange.
- Read: `select=id,title` (embed: `author(name)`), `order=created_at.desc`, `limit=20&offset=40` (limit defaults to 30 and is capped), or keyset `first=20&after=<value>`.
- Filters: `<column>=<op>.<value>` with `eq. neq. gt. gte. lt. lte. like. ilike. in.(a,b) is.null`, `not.` before any, and `or=(a.eq.1,b.gt.2)`.
- Writes: `POST` an object or an array; `PATCH` and `DELETE` need at least one filter; `Prefer: resolution=merge-duplicates` upserts one object.
- `Prefer: return=representation` returns written rows. `Prefer: count=exact` adds `pagination.total` and `Content-Range`.
- A list answers `{"data":[...]}`. With a count it answers `{"data":[...],"pagination":{"total","limit","offset"}}`. With `first`/`after` it answers `{"data":[...],"pageInfo":{"hasNextPage"}}`.
- **Counting rows needs `allowAggregations: true` on the role's select permission.** Without it, a request with `Prefer: count=exact` answers 403 `permission_denied` "Counting rows of ... is not permitted".

### Probing it: `test_api_request`

`test_api_request` sends one request the way a page does. With neither a key nor an access token it sends no credential at all, which runs as anon (ADR 0036). Otherwise it exchanges a publishable key (`esk_pub_...`, never a secret key) for the anon role's token, then calls REST or GraphQL with the page's query, `Prefer` and `Origin`. It returns the status, the CORS and count headers, and the body, cut at 8 KiB. Permissions, RLS and CORS apply as they do for the page.

The target is always `{data plane}/{projectId}/...`, built from the server's setting and the project id. The caller picks only the table, the query and the headers, redirects are not followed, and a GraphQL body is re-encoded before it is sent. When the browser check says an origin is blocked but it is already on the allowlist, the answer says to wait about a minute: CORS changes take that long to reach the data plane.

- A read-only connection sends only `GET`/`HEAD`, or GraphQL with no mutation.
- Each user gets 10 probes at once, then one every 2 seconds: every probe leaves from the platform's own address.
- The data plane is `MCP_DATA_PLANE_URL`, else an explicitly set `PUBLIC_BASE_URL`; with neither the tool answers that it is not available. An in-cluster `MCP_DATA_PLANE_URL` must be the same gateway the public edge uses, routing both `/auth/...` and `/{projectId}/...`, or the probe sees something different from a browser.
- It probes table REST and GraphQL only; `PUT`, `rpc/` and non-public schemas are not covered. `Prefer: tx=rollback` tries a write without keeping it.

## Connecting a client

With the token in `EXCALIBASE_TOKEN`:

Claude Code

```sh
claude mcp add --transport http excalibase "https://app.excalibase.io/mcp?project=<id>" --header "Authorization: Bearer $EXCALIBASE_TOKEN"
```

Cursor, `.cursor/mcp.json`

```json
{ "mcpServers": { "excalibase": { "url": "https://app.excalibase.io/mcp?project=<id>", "headers": { "Authorization": "Bearer <token>" } } } }
```

Codex, `~/.codex/config.toml`

```toml
[mcp_servers.excalibase]
url = "https://app.excalibase.io/mcp?project=<id>"
bearer_token_env_var = "EXCALIBASE_TOKEN"
# codex exec cannot ask for approval: without this every write tool is refused client-side.
default_tools_approval_mode = "approve"
```

For a one-off run, the same settings go on the command line, for example
`codex exec -c 'mcp_servers.excalibase.url="..."' -c 'mcp_servers.excalibase.bearer_token_env_var="EXCALIBASE_TOKEN"' -c 'mcp_servers.excalibase.default_tools_approval_mode="approve"' "..."`.

Gemini CLI, `.gemini/settings.json`

```json
{ "mcpServers": { "excalibase": { "httpUrl": "https://app.excalibase.io/mcp?project=<id>", "headers": { "Authorization": "Bearer <token>" } } } }
```

Add `&read_only=true` to the URL for a read-only connection.

## Activity

Studio's AI Tools page (project, AI Tools) creates the token, shows the setup for
each client, and lists the project's MCP calls: tool, token name, time and result
(`GET /api/projects/{id}/ai-activity/`, Developer+). A call made with one of your
own tokens can be revoked from the list; another member's token id is never shown.
