// Sites router (EXC-353, ADR 0043): <site>.<BASE_DOMAIN> → that site's user
// Worker in the Workers for Platforms dispatch namespace.
//
// Bindings (set by deploy.sh):
//   SITES        dispatch namespace holding one `site-<site id>` Worker per site
//   SITE_HOSTS   KV: hostname → script name (static site), or a JSON entry
//                {script, project, cpuMs, subRequests, paused} (server site, EXC-661),
//                written by the platform on deploy
//   BASE_DOMAIN  the user-content domain, e.g. excalibase.app
//   ABUSE_URL    https: or mailto: link shown on the 404 page
//   OUTBOUND     set when the namespace has the outbound Worker bound (deploy.sh --outbound)

// Same shape the platform accepts for a site name (server-go/internal/sites ValidateName).
const SITE_LABEL = /^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/;
// The platform names a site's Worker "site-" + its id; script names stop at 63 characters.
const SITE_SCRIPT = /^site-[a-z0-9][a-z0-9-]{0,57}$/;
// A customer's own hostname (EXC-614): the platform writes it into SITE_HOSTS
// only once Cloudflare for SaaS reports it and its certificate active.
const CUSTOM_HOSTNAME = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;
const INTERNAL_HEADER_PREFIX = "x-excalibase-";
// Headers a server site's code may not answer with: ours, and Cloudflare's own.
const RESERVED_RESPONSE_PREFIXES = [INTERNAL_HEADER_PREFIX, "cf-"];
// The platform's project ids: one path segment, nothing to escape.
const PROJECT_ID = /^[A-Za-z0-9_-]{1,64}$/;
// Cloudflare's own ceilings for one request of a user Worker.
const MAX_CPU_MS = 300000;
const MAX_SUBREQUESTS = 10000;
// KV's minimum: an unrouted host keeps serving for at most this long. KV bills
// each get the same whether it is answered from cache or not.
const LOOKUP_CACHE_SECONDS = 30;

export function siteLabel(hostname, baseDomain) {
  const suffix = `.${baseDomain}`;
  if (!hostname.endsWith(suffix)) {
    return null;
  }
  const label = hostname.slice(0, -suffix.length);
  if (!SITE_LABEL.test(label) || label.includes("--")) {
    return null;
  }
  return label;
}

export default {
  async fetch(request, env) {
    const baseDomain = typeof env.BASE_DOMAIN === "string" ? env.BASE_DOMAIN.trim().toLowerCase() : "";
    if (!baseDomain || !env.SITES || !env.SITE_HOSTS) {
      console.error("sites router: BASE_DOMAIN, SITES or SITE_HOSTS is not bound");
      return plainError(503, "This site is temporarily unavailable.");
    }
    const hostname = new URL(request.url).hostname;
    const key = hostKey(hostname, baseDomain);
    if (!key) {
      return notFound(env.ABUSE_URL);
    }
    const entry = await lookupEntry(env.SITE_HOSTS, key);
    if (!entry) {
      return notFound(env.ABUSE_URL);
    }
    // Server code never runs with unfiltered egress (deploy.sh --outbound binds the filter).
    if (entry.limits && !env.OUTBOUND) {
      console.error(`sites router: ${hostname} is a server site but no outbound Worker is bound; refusing it`);
      return plainError(503, "This site is temporarily unavailable.");
    }
    if (entry.paused) {
      return pausedPage();
    }
    let response;
    try {
      response = await siteWorker(env, entry).fetch(forwardable(request));
    } catch (err) {
      if (String(err?.message).startsWith("Worker not found")) {
        return notFound(env.ABUSE_URL);
      }
      console.error(`sites router: ${hostname}: ${err?.stack ?? err}`);
      return plainError(502, "This site could not be reached.");
    }
    return withSiteHeaders(response, hostname, entry.limits !== undefined);
  },
};

// siteWorker is the site's user Worker; a server site runs under its plan's limits,
// and its outbound fetches carry the site's identity to the outbound Worker.
function siteWorker(env, entry) {
  if (!entry.limits) {
    return env.SITES.get(entry.script);
  }
  const outbound = { siteId: entry.script.slice("site-".length), projectId: entry.project };
  return env.SITES.get(entry.script, {}, { limits: entry.limits, outbound });
}

// hostKey is the SITE_HOSTS key for a request's host: <site>.<base domain>, or
// a custom hostname outside the base domain. Anything else under the base
// domain (the apex, nested names) has no site.
export function hostKey(hostname, baseDomain) {
  const label = siteLabel(hostname, baseDomain);
  if (label) {
    return `${label}.${baseDomain}`;
  }
  if (hostname === baseDomain || hostname.endsWith(`.${baseDomain}`)) {
    return null;
  }
  return CUSTOM_HOSTNAME.test(hostname) ? hostname : null;
}

async function lookupEntry(table, hostname) {
  const value = await table.get(hostname, { cacheTtl: LOOKUP_CACHE_SECONDS });
  if (value === null) {
    return null;
  }
  const entry = hostEntry(value);
  if (!entry) {
    console.error(`sites router: ${hostname} maps to a value that is not a site entry; refusing it`);
  }
  return entry;
}

// hostEntry reads a SITE_HOSTS value: a plain script name is a static site; a
// JSON entry is a server site, refused whole unless every field is sound, so a
// bad entry never runs without its limits.
export function hostEntry(value) {
  if (!value.startsWith("{")) {
    return SITE_SCRIPT.test(value) ? { script: value } : null;
  }
  let parsed;
  try {
    parsed = JSON.parse(value);
  } catch {
    return null;
  }
  const { script, project, cpuMs, subRequests, paused = false } = parsed;
  const sound = typeof script === "string" && SITE_SCRIPT.test(script) &&
    typeof project === "string" && PROJECT_ID.test(project) &&
    withinLimit(cpuMs, MAX_CPU_MS) && withinLimit(subRequests, MAX_SUBREQUESTS) && typeof paused === "boolean";
  return sound ? { script, project, paused, limits: { cpuMs, subRequests } } : null;
}

function withinLimit(value, ceiling) {
  return Number.isInteger(value) && value >= 1 && value <= ceiling;
}

// forwardable drops the platform's own header namespace, so a visitor cannot
// pass a site off as another; everything else is the visitor's own request.
function forwardable(request) {
  const headers = new Headers();
  for (const [name, value] of request.headers) {
    if (!name.startsWith(INTERNAL_HEADER_PREFIX)) {
      headers.append(name, value);
    }
  }
  return new Request(request, { headers });
}

function withSiteHeaders(response, hostname, ranCode) {
  const headers = siteHeaders(response.headers, hostname, ranCode);
  if (response.status === 101) {
    // A handshake sets cookies like any answer; only its socket is carried over.
    return new Response(null, { status: 101, webSocket: response.webSocket, headers });
  }
  return new Response(response.body, { status: response.status, statusText: response.statusText, headers });
}

// siteHeaders are the headers a site's answer may carry: never the platform's
// or Cloudflare's namespaces from code, never a cookie for another host.
export function siteHeaders(source, hostname, ranCode) {
  const headers = new Headers(source);
  if (ranCode) {
    for (const name of [...headers.keys()]) {
      if (RESERVED_RESPONSE_PREFIXES.some((prefix) => name.startsWith(prefix))) {
        headers.delete(name);
      }
    }
  }
  headers.set("x-content-type-options", "nosniff");
  if (!headers.has("referrer-policy")) {
    headers.set("referrer-policy", "strict-origin-when-cross-origin");
  }
  const cookies = headers.getSetCookie();
  if (cookies.length > 0) {
    headers.delete("set-cookie");
    for (const cookie of cookies.filter((cookie) => cookieStaysOnHost(cookie, hostname))) {
      headers.append("set-cookie", cookie);
    }
  }
  return headers;
}

// cookieStaysOnHost refuses a Domain attribute naming anything but the site's
// own host: until the user-content domain is on the Public Suffix List, a
// Domain=<base domain> cookie would reach every other site.
function cookieStaysOnHost(cookie, hostname) {
  for (const attribute of cookie.split(";").slice(1)) {
    const [name, ...rest] = attribute.split("=");
    if (name.trim().toLowerCase() === "domain") {
      const domain = rest.join("=").trim().toLowerCase().replace(/^\./, "");
      if (domain !== hostname) {
        return false;
      }
    }
  }
  return true;
}

function plainError(status, message) {
  return new Response(message, {
    status,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": "no-store",
      "x-content-type-options": "nosniff",
    },
  });
}

// pausedPage answers a server site that used its month (EXC-661); its code does not run.
function pausedPage() {
  return new Response("This site is paused until its plan's monthly allowance renews.", {
    status: 503,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": "no-store",
      "x-content-type-options": "nosniff",
    },
  });
}

function notFound(abuseUrl) {
  const report = safeAbuseUrl(abuseUrl);
  const reportLine = report
    ? `<p>Seeing something harmful on a site here? Please <a href="${escapeHtml(report)}">report abuse</a>.</p>`
    : "";
  const body = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Site not found</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;color:#222;background:#fff}
@media (prefers-color-scheme:dark){body{color:#ddd;background:#111}a{color:#8ab4f8}}</style>
</head>
<body>
<h1>Site not found</h1>
<p>There is no site at this address.</p>
${reportLine}
</body>
</html>
`;
  return new Response(body, {
    status: 404,
    headers: {
      "content-type": "text/html; charset=utf-8",
      "cache-control": "no-store",
      "x-content-type-options": "nosniff",
      "referrer-policy": "no-referrer",
      "content-security-policy": "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'",
    },
  });
}

function safeAbuseUrl(value) {
  if (typeof value !== "string") {
    return null;
  }
  const trimmed = value.trim();
  return /^(https:\/\/|mailto:)\S+$/i.test(trimmed) ? trimmed : null;
}

function escapeHtml(text) {
  return text
    .replaceAll("&", "&amp;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;");
}
