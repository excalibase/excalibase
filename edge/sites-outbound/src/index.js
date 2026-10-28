// Sites outbound Worker (EXC-661, ADR 0045 decision 5): bound on the dispatch
// namespace, it sees every fetch() a server site's code makes. The router
// passes the site's identity per call; nothing here is read from the request.
//
// Bindings (set by deploy.sh):
//   API_HOST        the public project API host, e.g. api.excalibase.io
//   PLATFORM_HOSTS  comma list of the platform's own hosts and addresses; ".example.io" also covers its subdomains
// Parameters (from the router, per call):
//   siteId, projectId

const INTERNAL_HEADER_PREFIX = "x-excalibase-";
const PROJECT_ID = /^[A-Za-z0-9_-]{1,64}$/;
// Each hop is a subrequest the site's own limit may not count, so few are followed.
const MAX_REDIRECTS = 5;
const FORWARDING_HEADERS = ["x-forwarded-for", "x-forwarded-host", "x-forwarded-proto", "forwarded", "x-real-ip", "true-client-ip", "cf-connecting-ip"];
const CREDENTIAL_HEADERS = ["authorization", "cookie", "proxy-authorization"];
const REDIRECTS = new Set([301, 302, 303, 307, 308]);

export default {
  async fetch(request, env) {
    const apiHost = typeof env.API_HOST === "string" ? env.API_HOST.trim().toLowerCase() : "";
    const platform = platformHosts(env.PLATFORM_HOSTS);
    if (!apiHost || !platform) {
      console.error("sites outbound: API_HOST or PLATFORM_HOSTS is not bound");
      return refusal(503, "Outbound requests are unavailable.");
    }
    const projectId = typeof env.projectId === "string" && PROJECT_ID.test(env.projectId) ? env.projectId : null;
    const siteId = typeof env.siteId === "string" ? env.siteId.slice(0, 64) : null;
    return send(request, { apiHost, platform, projectId, siteId });
  },
};

// send makes the request, following redirects itself so every hop is checked.
async function send(original, rules) {
  let request = original;
  for (let hop = 0; hop <= MAX_REDIRECTS; hop++) {
    const checked = checkedRequest(request, rules);
    if (checked instanceof Response) {
      return checked;
    }
    const response = await fetch(checked, { redirect: "manual" });
    if (original.redirect !== "follow") {
      return response;
    }
    const location = response.headers.get("location");
    if (!REDIRECTS.has(response.status) || !location) {
      return response;
    }
    const next = redirected(request, response.status, new URL(location, request.url));
    if (!next) {
      return response;
    }
    request = next;
  }
  return refusal(508, "Too many redirects.");
}

// checkedRequest answers the request to send, rebuilt from what the code
// gave (no cf options ride along), or the refusal.
function checkedRequest(request, rules) {
  const url = new URL(request.url);
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    return refused(rules, "scheme", url.hostname, "Only http and https requests are allowed from a site's server code.");
  }
  const host = canonicalHost(url.hostname);
  if (host === null || blockedAddress(host)) {
    return refused(rules, "address", url.hostname, "Private and reserved addresses are not reachable from a site's server code.");
  }
  if (host === rules.apiHost) {
    if (!rules.projectId || !ownProjectPath(url.pathname, rules.projectId)) {
      return refused(rules, "api", host, "A site's server code may call only its own project's API.");
    }
    return rebuilt(request, url, apiHeaders(request.headers));
  }
  if (isPlatformHost(host, rules.platform)) {
    return refused(rules, "platform", host, "The platform's own hosts are not reachable from a site's server code.");
  }
  return rebuilt(request, url, new Headers(request.headers));
}

// canonicalHost drops a root-zone dot, so "api.example.io." is matched as the
// name it stands for; empty labels are no name at all.
function canonicalHost(hostname) {
  if (hostname === "" || hostname.startsWith(".") || hostname.includes("..")) {
    return null;
  }
  return hostname.toLowerCase().replace(/\.$/, "");
}

function rebuilt(request, url, headers) {
  const init = { method: request.method, headers, redirect: "manual" };
  if (request.body !== null) {
    // duplex is what a streamed body needs outside Workers; Workers ignore it.
    Object.assign(init, { body: request.body, duplex: "half" });
  }
  return new Request(url, init);
}

function refused({ siteId }, reason, host, message) {
  console.log(JSON.stringify({ refused: reason, site: siteId, host }));
  return refusal(403, message);
}

// A project API path: /<p>/graphql, /<p>/api/..., /auth/<p>/..., /functions/v1/<p>/...,
// /storage/v1/<p>/...; only plain segments, so no proxy can read it as another path.
const PLAIN_SEGMENT = /^[A-Za-z0-9_~-][A-Za-z0-9._~-]*$/;

// ownProjectPath is a path of the project's own public API, as the ingress routes it.
function ownProjectPath(pathname, projectId) {
  const segments = pathname.split("/").slice(1);
  if (!segments.every((segment) => PLAIN_SEGMENT.test(segment) && segment !== "." && segment !== "..")) {
    return false;
  }
  const [first, second, third] = segments;
  if (first === projectId) {
    return second === "graphql" || second === "api";
  }
  if (first === "auth") {
    return second === projectId;
  }
  if ((first === "functions" || first === "storage") && second === "v1") {
    return third === projectId;
  }
  return false;
}

// apiHeaders drop the platform's namespace and every header that names a
// client, so code cannot pass itself off as another caller.
function apiHeaders(source) {
  const headers = new Headers();
  for (const [name, value] of source) {
    if (!name.startsWith(INTERNAL_HEADER_PREFIX) && !FORWARDING_HEADERS.includes(name)) {
      headers.append(name, value);
    }
  }
  return headers;
}

// redirected is the request a redirect asks for: 303 and a POST behind 301/302
// become a GET without a body; a body cannot be sent twice, so 307/308 with one
// stops. Credentials never follow to another origin.
function redirected(request, status, target) {
  const headers = new Headers(request.headers);
  if (target.origin !== new URL(request.url).origin) {
    for (const name of CREDENTIAL_HEADERS) {
      headers.delete(name);
    }
  }
  const toGet = status === 303 || ((status === 301 || status === 302) && request.method === "POST");
  if (toGet) {
    headers.delete("content-type");
    headers.delete("content-length");
    return new Request(target, { method: request.method === "HEAD" ? "HEAD" : "GET", headers, redirect: "manual" });
  }
  if (request.body !== null) {
    return null;
  }
  return new Request(target, { method: request.method, headers, redirect: "manual" });
}

function platformHosts(raw) {
  if (typeof raw !== "string" || raw.trim() === "") {
    return null;
  }
  return raw.split(",").map((entry) => entry.trim().toLowerCase()).filter(Boolean);
}

function isPlatformHost(host, platform) {
  const v6 = host.startsWith("[") ? expandIPv6(host.slice(1, -1)) : null;
  return platform.some((entry) => {
    if (entry.includes(":")) {
      const listed = expandIPv6(entry.replace(/^\[|\]$/g, ""));
      return v6 !== null && listed !== null && listed.every((group, i) => group === v6[i]);
    }
    return entry.startsWith(".") ? host === entry.slice(1) || host.endsWith(entry) : host === entry;
  });
}

// blockedAddress refuses local names and IP literals outside the public internet.
export function blockedAddress(host) {
  if (host === "localhost" || /\.(localhost|local|internal)$/.test(host)) {
    return true;
  }
  if (host.startsWith("[")) {
    return !publicIPv6(host.slice(1, -1));
  }
  const octets = ipv4(host);
  return octets !== null && !publicIPv4(octets);
}

function ipv4(host) {
  const parts = host.split(".");
  if (parts.length !== 4 || !parts.every((part) => /^\d{1,3}$/.test(part))) {
    return null;
  }
  const octets = parts.map(Number);
  return octets.every((octet) => octet <= 255) ? octets : null;
}

// Ranges that are not the public internet (RFC 6890 and friends).
const PRIVATE_V4 = [
  [0, 0, 8], [10, 0, 8], [100, 64, 10], [127, 0, 8], [169, 254, 16], [172, 16, 12], [192, 0, 24],
  [192, 0, 24, 2], [192, 168, 16], [198, 18, 15], [198, 51, 24, 100], [203, 0, 24, 113], [224, 0, 4], [240, 0, 4],
];

function publicIPv4([a, b, c]) {
  const value = ((a << 24) >>> 0) + (b << 16) + (c << 8);
  for (const [first, second, bits, third = 0] of PRIVATE_V4) {
    const base = ((first << 24) >>> 0) + (second << 16) + (third << 8);
    const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
    if (((value & mask) >>> 0) === ((base & mask) >>> 0)) {
      return false;
    }
  }
  return true;
}

// publicIPv6 allows global unicast (2000::/3) only, without the documentation, 6to4 and Teredo ranges.
function publicIPv6(address) {
  const groups = expandIPv6(address);
  if (!groups) {
    return false;
  }
  const [first, second] = groups;
  if ((first & 0xe000) !== 0x2000) {
    return false;
  }
  if (first === 0x2002 || (first === 0x2001 && (second === 0x0db8 || second === 0x0000))) {
    return false;
  }
  return true;
}

function expandIPv6(address) {
  if (address.includes(".")) {
    return null;
  }
  const [head, tail, extra] = address.split("::");
  if (extra !== undefined) {
    return null;
  }
  const left = head ? head.split(":") : [];
  const right = tail === undefined ? [] : tail ? tail.split(":") : [];
  const missing = 8 - left.length - right.length;
  if ((tail === undefined && missing !== 0) || missing < 0) {
    return null;
  }
  const groups = [...left, ...Array(tail === undefined ? 0 : missing).fill("0"), ...right];
  if (!groups.every((group) => /^[0-9a-f]{1,4}$/i.test(group))) {
    return null;
  }
  return groups.map((group) => parseInt(group, 16));
}

function refusal(status, message) {
  return new Response(message, {
    status,
    headers: { "content-type": "text/plain; charset=utf-8", "x-content-type-options": "nosniff" },
  });
}
