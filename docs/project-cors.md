# Per-project CORS — the browser-origin allowlist

The data plane a project exposes (`/{projectId}/graphql`,
`/{projectId}/api/v1/*`, the WebSocket upgrades on those paths, and the
project's public functions at `/functions/v1/{projectId}/*`) is called
straight from browsers, so it must answer CORS. It does so **per project**:
each project carries its own list of allowed origins, stored by provisioning
and enforced by excalibase-graphql (GraphQL, REST) and by provisioning
(functions, read on every call). A project starts with **no origins** —
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

## Native apps (Capacitor, Ionic, Tauri, Electron)

A native app's WebView preflights like any browser, so its origin goes on the
list like a website's:

| Runtime | Origin to add |
|---|---|
| Capacitor, iOS | `capacitor://localhost` |
| Capacitor, Android | `https://localhost` (or `http://localhost` with `androidScheme: "http"`) |
| Ionic legacy WebView | `ionic://localhost` |
| Tauri v2, macOS/Linux | `tauri://localhost` |
| Tauri v2, Windows | `http://tauri.localhost` (`https://` with `useHttpsScheme`) |
| Electron, custom protocol | the origin you register, e.g. `app://bundle` |
| Electron `loadFile` / any `file://` page | sends `null`, which cannot be listed: every sandboxed iframe on the web sends it too. Register a custom protocol instead. |

From an agent: `add_cors_origin` with the origin above.

## The rule every service applies

GraphQL, REST, auth and functions answer the same way (EXC-563):

| Request | Origin listed | Origin not listed |
|---|---|---|
| Preflight | grant headers | 403, no CORS headers — the browser blocks the call |
| Actual request | served, `Access-Control-Allow-Origin` echoed | served, no `Access-Control-Allow-Origin` — a page cannot read it |
| WebSocket upgrade from a web page (`http`/`https`) | 101 | 403 |
| WebSocket upgrade with no `Origin` or a native scheme / `null` | 101 | 101 |

These APIs take a bearer token, never a cookie, so a foreign page has no
ambient credential to ride and CORS is the browser's protection. Refusing an
actual request for its `Origin` alone would add nothing (any non-browser
client can omit or forge the header) and would break native apps and
server-side proxies. Browsers apply no CORS to WebSockets, so the engine
checks web origins at the upgrade itself. Studio's own `/api` is different: it
authenticates with a session cookie, so it refuses a cookie-authenticated
write from an untrusted origin on the server.

## How it reaches the data plane

```
PUT /api/projects/{id}/cors ──► project_cors_settings (platform Postgres)
                                        │
GET /api/projects/{id}/info ◄───────────┘  corsAllowedOrigins: [...]
        ▲
        │ on demand, cached 30 s per project (service PAT)
excalibase-graphql ── request /{id}/graphql with Origin: https://app.example.com
                      └─ origin on the list → Access-Control-Allow-Origin echoed
                         not on the list / list empty → no CORS headers (403 on preflight,
                                                         actual request served)
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
