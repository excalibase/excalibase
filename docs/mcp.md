# MCP for coding tools

Excalibase serves an MCP endpoint (streamable HTTP) at `https://<studio host>/mcp`
(`https://app.excalibase.io/mcp` on the hosted platform). AI coding tools use it to
read the schema, run SQL and migrations, generate types, set API permissions, deploy
functions and apps, and read logs (ADR 0037, decision 6).

## Authentication

Send a personal access token in `Authorization: Bearer <token>`. Sign-in sessions,
cookies and service tokens are refused. Studio's "Connect your AI tool" page mints a
token bound to one project.

Two query parameters can only narrow what the token allows:

| Parameter | Effect |
|---|---|
| `read_only=true` | No write tool is offered; `execute_sql` runs one statement in a read-only transaction on a connection that is closed afterwards, and refuses functions that act outside it (`pg_terminate_backend`, `dblink*`, `lo_*`, `pg_notify`, ...). A token with only the `read` scope is always read-only. |
| `project=<id>` | Every tool acts on that project only. A token bound to another project is refused. |

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
| `get_project_info` | `GET /api/projects/{id}/info/`, `GET /api/projects/{id}/sdk-keys/` |
| `create_publishable_key` | `POST /api/projects/{id}/sdk-keys/` (publishable only) |
| `list_tables` | `GET /api/schema/{id}/tables` |
| `describe_table` | `GET /api/schema/{id}/tables/{t}/columns`, `.../indexes`, `GET /api/schema/{id}/relationships` |
| `get_graphql_schema` | the tables, columns and foreign keys the API is generated from, plus the introspection command |
| `generate_typescript_types` | `GET /api/schema/{id}/tables` and each table's columns |
| `execute_sql` | `POST /api/schema/{id}/query`; read-only: `GET /api/schema/{id}/query?sql=` |
| `list_migrations` / `apply_migration` | `GET` / `POST /api/provision/{id}/migrations/` |
| `list_permissions` / `set_permission` | `GET /api/provision/{id}/permissions/`, `PUT`/`DELETE .../permissions/tables/{t}/roles/{r}/{op}` |
| `list_functions` / `deploy_function` / `set_function_secret` | `GET`/`POST /api/projects/{id}/functions/`, `POST .../functions/secrets` |
| `list_apps` / `deploy_app` / `get_deploy_status` | `GET /api/projects/{id}/apps/`; `POST .../apps/{app}/deploy` with `{image, commitSha}` (resolved to a digest) or empty to redeploy; `GET .../apps/{app}/` + `.../deploys` or `.../deploys/{deployId}` |
| `get_logs` | database: `GET /api/provision/{id}/logs`; app: `.../apps/{a}/logs`; function: `.../functions/{f}/logs` |
| `get_dockerfile_template` | none: Dockerfiles for node, nextjs, vite, python, go, java |
| `get_ci_snippet` | `GET .../apps/{app}/` for the app's image; renders the same GitHub Actions, GitLab CI, Jenkins or curl pipeline as Studio's pipeline page |

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
```

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
