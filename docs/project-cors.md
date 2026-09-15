# Per-project CORS — the browser-origin allowlist

The data plane a project exposes (`/{projectId}/graphql`,
`/{projectId}/api/v1/*` and the WebSocket upgrades on those paths) is called
straight from browsers, so it must answer CORS. It does so **per project**:
each project carries its own list of allowed origins, stored by provisioning
and enforced by excalibase-graphql. A project starts with **no origins** —
browser apps are blocked until the project opts them in.

## Set the allowlist

```bash
# Developer+ on the project's org (same gate as the other data-plane authoring calls)
curl -sf -X PUT -H "Authorization: Bearer $PAT" -H "Content-Type: application/json" \
  "https://<host>/api/projects/<projectId>/cors" \
  -d '{"allowedOrigins": ["https://app.example.com", "http://localhost:5173"]}'
```

```json
{
  "allowedOrigins": ["http://localhost:5173", "https://app.example.com"],
  "allowWildcard": false
}
```

* `allowedOrigins` — the project's setting; `PUT` replaces it whole. Send
  `[]` to block browsers again.
* `allowWildcard` — read-only in the response: `true` when the list is the
  single `"*"` entry.

`GET /api/projects/{projectId}/cors` returns the same shape.

## What an entry may be

| Accepted | Stored as | Note |
|---|---|---|
| `https://app.example.com` | `https://app.example.com` | |
| `HTTPS://App.Example.com:443` | `https://app.example.com` | lower-cased; the scheme's default port is dropped because browsers omit it from `Origin` |
| `http://localhost:5173` | `http://localhost:5173` | non-default ports are kept |
| `capacitor://localhost` | `capacitor://localhost` | any scheme with a host |
| `*` with `"allowWildcard": true` | `*` | must be the only entry |

Refused (400, the error names the entry): a bare hostname (`app.example.com`),
anything with a path (`https://app.example.com/`), query, fragment or
userinfo, a subdomain wildcard (`https://*.example.com`), `null`, `"*"`
without `allowWildcard` or next to other entries, and more than 32 entries.

## How it reaches the data plane

```
PUT /api/projects/{id}/cors ──► project_cors_settings (platform Postgres)
                                        │
GET /api/projects/{id}/info ◄───────────┘  corsAllowedOrigins: [...]
        ▲
        │ on demand, cached 30 s per project (service PAT)
excalibase-graphql ── request /{id}/graphql with Origin: https://app.example.com
                      └─ origin on the list → Access-Control-Allow-Origin echoed
                         not on the list / list empty → no CORS headers (403 on preflight)
```

* The engine resolves the project from the URL path exactly as it does for
  RLS, fetches `/info` when its 30 s cache for that project has expired, and
  compares the request's `Origin` byte-for-byte with the list.
* **Fail-closed**: if provisioning is unreachable the engine keeps serving
  the last list it saw for the project; a project it has never resolved is
  denied. It never falls back to `*`.
* Non-project routes on the engine (health, actuator) keep the engine's
  global `app.cors.allowed-origins` setting.
* Credentials are never allowed (`Access-Control-Allow-Credentials` is not
  sent): auth is a Bearer token, not a cookie.

## Default for existing projects

Projects created before this feature have no row and therefore **no
origins** — a browser app that relied on the engine's previous global `*`
must have its origin added. Non-browser clients are unaffected.
