# Edge function responses — which headers survive

Functions answer on the platform's shared API origin, so the gateway
(`writeRuntimeResponse` in server-go) filters what a function may set. Status,
body and ordinary headers (`Content-Type`, `Cache-Control`, custom `X-*`) pass
through unchanged.

* **Cookies.** A function may set host-only cookies for itself. The gateway
  pins their `Path` to `/functions/v1/<project>/` and keeps only `Expires`,
  `Max-Age`, `Secure`, `HttpOnly`, `SameSite` and `Partitioned`. A cookie with a
  `Domain` attribute, a `__Host-` or `__Secure-` name, or one of the platform's
  own names (`excali_session` and the other `excali_*` cookies) is dropped.
  Read a function cookie from a request to `/functions/v1/<project>/...` only.
* **Policy headers.** `Content-Security-Policy(-Report-Only)`, `X-Frame-Options`,
  `Strict-Transport-Security`, `Permissions-Policy`, `Cross-Origin-*`,
  `Clear-Site-Data`, `Report-To`, `Reporting-Endpoints` and `NEL` are stripped.
* **Sandbox.** Every function answer carries the platform's policy plus
  `sandbox allow-scripts allow-forms` (no `allow-same-origin`) and
  `X-Content-Type-Options: nosniff`. Any HTML a function serves runs in an
  opaque origin; inline scripts still do not run (`script-src 'self'`). Functions
  are for APIs and webhooks. Host web pages as container apps.
