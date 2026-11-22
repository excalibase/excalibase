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
  `Strict-Transport-Security`, `Permissions-Policy`, `Cross-Origin-*`, `Referrer-Policy`, `Service-Worker-Allowed`, `Set-Cookie2`,
  `Clear-Site-Data`, `Report-To`, `Reporting-Endpoints` and `NEL` are stripped.
* **Sandbox.** Every function answer carries the platform's policy plus
  `sandbox allow-scripts` (no `allow-same-origin`, `allow-forms`, popups or top navigation) and
  `X-Content-Type-Options: nosniff`. Any HTML a function serves runs in an
  opaque origin; inline scripts still do not run (`script-src 'self'`). Functions
  are for APIs and webhooks. Host web pages as container apps.

Consequences to know:

* Only the **last** `Set-Cookie` of a response survives: the Deno runtime
  collapses repeated headers into one map entry.
* Scripts served from the same host do run, in an opaque origin. Forms cannot
  submit, and a `fetch` from a function page sends `Origin: null`.
* A function can still set its own CORS headers on its answers.
* A service worker cannot be registered from a function page (opaque origin, no
  `Service-Worker-Allowed`). A request marked `Service-Worker: script` to a
  function URL is answered 404, so a worker registered before this change is
  unregistered by the browser at its next update check.

## Pages are served as plain text

Functions return data, not pages. On the shared API origin a function answer
is served as `text/plain; charset=utf-8`, body unchanged, when its
`Content-Type` is `text/html`, `application/xhtml+xml`, `image/svg+xml`, any
other XML type (`text/xml`, `application/xml`, `text/xsl`, `text/rdf`, anything ending in
`+xml`, so RSS, Atom and sitemaps too) or any `multipart/*`. This stops a fake
login page being served from the platform's own host (the same rule Supabase
applies to functions without a custom domain). Case and parameters such as
`; charset=utf-8` do not matter.

* XML is not inspected: a browser reaches XHTML or SVG through character
  references, entities, UTF-16 bodies and `xml-stylesheet`, so every XML type is
  plain text. Serve a sitemap or feed from an app or a site.
* Every `Content-Type` the function sent is read (the runtime joins repeated
  headers); a blocked one anywhere is enough.
* An answer with no `Content-Type`, or one a browser cannot place (`*/*`,
  `unknown/unknown`, `application/unknown`, anything that does not parse), gets
  one fixed by the gateway from its body, so it cannot be sniffed into a page.
* It applies to every status, including redirects, and to `HEAD` as well as `GET`.
  `X-Content-Type-Options: nosniff` is always set.
* Functions have no custom domain today (they are served only at
  `/functions/v1/<project>/`), so there is no domain on which page types pass
  through as written.
* To serve a page, a sitemap or a feed, host it as a container app or a site. A
  function can still return JSON, plain text, CSV and other data types, and the
  page can `fetch` them.
* Operators: `FEATURE_FUNCTIONHTMLGUARD` is on unless set to `false`.
