// Sites router (EXC-353, ADR 0043): <site>.<BASE_DOMAIN> → that site's user
// Worker in the Workers for Platforms dispatch namespace.
//
// Bindings (set by deploy.sh):
//   SITES        dispatch namespace holding one `site-<site id>` Worker per site
//   SITE_HOSTS   KV: hostname → script name, written by the platform on deploy
//   BASE_DOMAIN  the user-content domain, e.g. excalibase.app
//   ABUSE_URL    https: or mailto: link shown on the 404 page

// Same shape the platform accepts for a site name (server-go/internal/sites ValidateName).
const SITE_LABEL = /^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/;
// The platform names a site's Worker "site-" + its id; script names stop at 63 characters.
const SITE_SCRIPT = /^site-[a-z0-9][a-z0-9-]{0,57}$/;
// A customer's own hostname (EXC-614): the platform writes it into SITE_HOSTS
// only once Cloudflare for SaaS reports it and its certificate active.
const CUSTOM_HOSTNAME = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;
const INTERNAL_HEADER_PREFIX = "x-excalibase-";
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
    const script = await lookupScript(env.SITE_HOSTS, key);
    if (!script) {
      return notFound(env.ABUSE_URL);
    }
    let response;
    try {
      response = await env.SITES.get(script).fetch(forwardable(request));
    } catch (err) {
      if (String(err?.message).startsWith("Worker not found")) {
        return notFound(env.ABUSE_URL);
      }
      console.error(`sites router: ${hostname}: ${err?.stack ?? err}`);
      return plainError(502, "This site could not be reached.");
    }
    return withSiteHeaders(response, hostname);
  },
};

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

async function lookupScript(table, hostname) {
  const script = await table.get(hostname, { cacheTtl: LOOKUP_CACHE_SECONDS });
  if (script === null) {
    return null;
  }
  if (!SITE_SCRIPT.test(script)) {
    console.error(`sites router: ${hostname} maps to a value that is not a site script; refusing it`);
    return null;
  }
  return script;
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

function withSiteHeaders(response, hostname) {
  if (response.status === 101) {
    return response;
  }
  const result = new Response(response.body, response);
  result.headers.set("x-content-type-options", "nosniff");
  if (!result.headers.has("referrer-policy")) {
    result.headers.set("referrer-policy", "strict-origin-when-cross-origin");
  }
  const cookies = result.headers.getSetCookie();
  if (cookies.length > 0) {
    result.headers.delete("set-cookie");
    for (const cookie of cookies.filter((cookie) => cookieStaysOnHost(cookie, hostname))) {
      result.headers.append("set-cookie", cookie);
    }
  }
  return result;
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
