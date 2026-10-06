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
| `list_apps` / `deploy_app` / `get_deploy_status` | `GET /api/projects/{id}/apps/` (+ `.../apps/{app}/deploys?limit=1` to report `NOT_DEPLOYED`); `POST .../apps/{app}/deploy` with `{image, commitSha}` or empty to run the app's own image, pinned to its digest either way when the registry is public (the result's `unpinned` says when it is not); `GET .../apps/{app}/` + `.../deploys` or `.../deploys/{deployId}` |
| `create_app` | `POST /api/projects/{id}/apps/` (Studio's plan limits apply), then `GET`/`PUT /api/projects/{id}/cors/` to allow the app's own origin (it stays listed after the app is deleted; remove it in Studio) |
| `get_logs` | database: `GET /api/provision/{id}/logs`; app: `.../apps/{a}/logs`; function: `.../functions/{f}/logs`. A log backend that does not answer gives "not available", never its address |
| `test_api_request` | `GET /api/projects/{id}/info/`, then one request to the project's own data API (below) |
| `get_dockerfile_template` | none: Dockerfiles for node, nextjs, vite, python, go, java |
| `get_ci_snippet` | `GET .../apps/{app}/` for the app's image; renders the same GitHub Actions, GitLab CI, Jenkins or curl pipeline as Studio's pipeline page |

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

`test_api_request` sends one request the way a page does. It exchanges a publishable key (`esk_pub_...`, never a secret key) for the anon role's token, then calls REST or GraphQL with the page's query, `Prefer` and `Origin`. It returns the status, the CORS and count headers, and the body, cut at 8 KiB. Permissions, RLS and CORS apply as they do for the page.

The target is always `{data plane}/{projectId}/...`, built from the server's setting and the project id. The caller picks only the table, the query and the headers, redirects are not followed, and a GraphQL body is re-encoded before it is sent.

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
