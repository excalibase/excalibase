import { describe, expect, it, vi } from "vitest";
import router, { hostKey, siteLabel } from "../src/index.js";

const BASE = "excalibase.app";
const SCRIPT = "site-0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c";

// fakeEnv stands in for the bindings deploy.sh gives the router: the dispatch
// namespace, the hostname table the platform writes, and two plain vars.
function fakeEnv({ hosts = { [`shop.${BASE}`]: SCRIPT }, site, overrides = {} } = {}) {
  const calls = { get: [], fetch: [], lookups: [] };
  const siteFetch = site ?? (async () => new Response("<h1>shop</h1>", { headers: { "content-type": "text/html" } }));
  const env = {
    BASE_DOMAIN: BASE,
    ABUSE_URL: "mailto:abuse@example.com",
    SITE_HOSTS: {
      async get(key, options) {
        calls.lookups.push({ key, options });
        return hosts[key] ?? null;
      },
    },
    SITES: {
      get(script, args, options) {
        calls.get.push({ script, args, options });
        return {
          async fetch(request) {
            calls.fetch.push(request);
            return siteFetch(request, script);
          },
        };
      },
    },
    ...overrides,
  };
  return { env, calls };
}

const workerNotFound = async () => {
  throw new Error("Worker not found.");
};

describe("siteLabel", () => {
  it("answers the one label in front of the base domain", () => {
    expect(siteLabel(`shop.${BASE}`, BASE)).toBe("shop");
    expect(siteLabel(`clash-of-pokemon.${BASE}`, BASE)).toBe("clash-of-pokemon");
  });

  it.each([
    [BASE, "the apex"],
    [`.${BASE}`, "an empty label"],
    [`a.b.${BASE}`, "two labels"],
    [`www.shop.${BASE}`, "a nested host"],
    [`shop.${BASE}.evil.com`, "the base domain inside another"],
    [`shopexcalibase.app`, "no dot before the base domain"],
    [`ab.${BASE}`, "a label shorter than a site name"],
    [`-shop.${BASE}`, "a leading hyphen"],
    [`shop-.${BASE}`, "a trailing hyphen"],
    [`xn--shop.${BASE}`, "two hyphens in a row"],
    [`my_shop.${BASE}`, "an underscore"],
    [`${"a".repeat(64)}.${BASE}`, "a label over 63 characters"],
    ["shop.workers.dev", "another domain"],
    [`shop.${BASE}.`, "a trailing dot"],
  ])("refuses %s (%s)", (host) => {
    expect(siteLabel(host, BASE)).toBeNull();
  });
});

describe("routing", () => {
  it("dispatches a site host to the script the platform recorded for it", async () => {
    const { env, calls } = fakeEnv();
    const response = await router.fetch(new Request(`https://shop.${BASE}/dashboard/1?x=1`), env);

    expect(response.status).toBe(200);
    expect(await response.text()).toBe("<h1>shop</h1>");
    expect(calls.lookups).toEqual([{ key: `shop.${BASE}`, options: { cacheTtl: 30 } }]);
    expect(calls.get.map((call) => call.script)).toEqual([SCRIPT]);
    expect(calls.fetch[0].url).toBe(`https://shop.${BASE}/dashboard/1?x=1`);
  });

  it("matches the base domain without regard to its case in the config", async () => {
    const { env, calls } = fakeEnv({ overrides: { BASE_DOMAIN: "Excalibase.App" } });
    const response = await router.fetch(new Request(`https://SHOP.excalibase.app/`), env);
    expect(response.status).toBe(200);
    expect(calls.lookups[0].key).toBe(`shop.${BASE}`);
  });

  it("gives the site Worker no arguments, so it sees none of the router's bindings", async () => {
    const { env, calls } = fakeEnv();
    await router.fetch(new Request(`https://shop.${BASE}/`), env);
    expect(calls.get[0].args).toBeUndefined();
    expect(calls.get[0].options).toBeUndefined();
  });

  it.each([`a.b.${BASE}`, BASE, `www.shop.${BASE}`])(
    "answers 404 for %s without looking anything up",
    async (host) => {
      const { env, calls } = fakeEnv();
      const response = await router.fetch(new Request(`https://${host}/`), env);
      expect(response.status).toBe(404);
      expect(calls.lookups).toEqual([]);
      expect(calls.get).toEqual([]);
    },
  );

  it("answers 404 for a site the platform has not recorded", async () => {
    const { env, calls } = fakeEnv();
    const response = await router.fetch(new Request(`https://nope.${BASE}/`), env);
    expect(response.status).toBe(404);
    expect(calls.get).toEqual([]);
  });

  it("answers 404 when the recorded Worker is gone", async () => {
    const { env } = fakeEnv({ site: workerNotFound });
    const response = await router.fetch(new Request(`https://shop.${BASE}/`), env);
    expect(response.status).toBe(404);
    expect(await response.text()).toContain("report abuse");
  });

  it.each(["../other", "site-a/b", "other-script", "", "SITE-ABC", `site-${"a".repeat(60)}`])(
    "refuses to dispatch to a recorded value that is not a site script (%j)",
    async (value) => {
      const { env, calls } = fakeEnv({ hosts: { [`shop.${BASE}`]: value } });
      const errors = vi.spyOn(console, "error").mockImplementation(() => {});
      const response = await router.fetch(new Request(`https://shop.${BASE}/`), env);
      expect(response.status).toBe(404);
      expect(calls.get).toEqual([]);
      errors.mockRestore();
    },
  );

  it("answers 502 without the cause when the site Worker fails", async () => {
    const { env } = fakeEnv({
      site: async () => {
        throw new Error("internal detail: namespace sites-prod account 123");
      },
    });
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const response = await router.fetch(new Request(`https://shop.${BASE}/`), env);
    expect(response.status).toBe(502);
    const body = await response.text();
    expect(body).not.toContain("internal detail");
    expect(body).not.toContain("sites-prod");
    expect(errors).toHaveBeenCalled();
    errors.mockRestore();
  });

  it.each([
    ["BASE_DOMAIN", undefined],
    ["BASE_DOMAIN", ""],
    ["SITES", undefined],
    ["SITE_HOSTS", undefined],
  ])("fails closed with 503 when %s is %j", async (name, value) => {
    const { env, calls } = fakeEnv();
    env[name] = value;
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const response = await router.fetch(new Request(`https://shop.${BASE}/`), env);
    expect(response.status).toBe(503);
    expect(calls.fetch).toEqual([]);
    errors.mockRestore();
  });
});

describe("what reaches the site Worker", () => {
  it("drops headers in the platform's own namespace, so a visitor cannot forge them", async () => {
    const { env, calls } = fakeEnv();
    await router.fetch(
      new Request(`https://shop.${BASE}/`, {
        headers: { "x-excalibase-site": "other", "X-Excalibase-Project": "p1", accept: "text/html" },
      }),
      env,
    );
    const forwarded = calls.fetch[0];
    expect(forwarded.headers.get("x-excalibase-site")).toBeNull();
    expect(forwarded.headers.get("x-excalibase-project")).toBeNull();
    expect(forwarded.headers.get("accept")).toBe("text/html");
  });

  it("forwards the method and body unchanged", async () => {
    const { env, calls } = fakeEnv();
    await router.fetch(new Request(`https://shop.${BASE}/form`, { method: "POST", body: "a=1" }), env);
    expect(calls.fetch[0].method).toBe("POST");
    expect(await calls.fetch[0].text()).toBe("a=1");
  });
});

describe("response headers", () => {
  it("adds nosniff and a referrer policy, and leaves the site's own headers alone", async () => {
    const { env } = fakeEnv({
      site: async () =>
        new Response("ok", { headers: { "content-type": "text/plain", "cache-control": "public, max-age=60" } }),
    });
    const response = await router.fetch(new Request(`https://shop.${BASE}/`), env);
    expect(response.headers.get("x-content-type-options")).toBe("nosniff");
    expect(response.headers.get("referrer-policy")).toBe("strict-origin-when-cross-origin");
    expect(response.headers.get("cache-control")).toBe("public, max-age=60");
    expect(response.headers.get("content-security-policy")).toBeNull();
    expect(response.headers.get("x-frame-options")).toBeNull();
  });

  it("keeps a referrer policy the site chose", async () => {
    const { env } = fakeEnv({ site: async () => new Response("ok", { headers: { "referrer-policy": "no-referrer" } }) });
    const response = await router.fetch(new Request(`https://shop.${BASE}/`), env);
    expect(response.headers.get("referrer-policy")).toBe("no-referrer");
  });

  it("keeps the site's status", async () => {
    const { env } = fakeEnv({ site: async () => new Response("missing", { status: 404 }) });
    const response = await router.fetch(new Request(`https://shop.${BASE}/x`), env);
    expect(response.status).toBe(404);
    expect(await response.text()).toBe("missing");
  });

  it("drops a cookie scoped wider than the site's own host, so one site cannot plant cookies on others", async () => {
    const headers = new Headers();
    headers.append("set-cookie", "a=1; Path=/");
    headers.append("set-cookie", `b=2; Domain=${BASE}; Path=/`);
    headers.append("set-cookie", `c=3; domain=.${BASE}`);
    headers.append("set-cookie", `d=4; Domain=shop.${BASE}`);
    headers.append("set-cookie", `e=5; Domain=other.${BASE}`);
    const { env } = fakeEnv({ site: async () => new Response("ok", { headers }) });
    const response = await router.fetch(new Request(`https://shop.${BASE}/`), env);
    expect(response.headers.getSetCookie()).toEqual(["a=1; Path=/", `d=4; Domain=shop.${BASE}`]);
  });
});

describe("the default 404 page", () => {
  it("is plain HTML with a report-abuse link from ABUSE_URL and no outside assets", async () => {
    const { env } = fakeEnv();
    const response = await router.fetch(new Request(`https://nope.${BASE}/`), env);
    expect(response.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(response.headers.get("cache-control")).toBe("no-store");
    expect(response.headers.get("x-content-type-options")).toBe("nosniff");
    expect(response.headers.get("content-security-policy")).toContain("default-src 'none'");
    const body = await response.text();
    expect(body).toContain('<a href="mailto:abuse@example.com">report abuse</a>');
    expect(body).not.toMatch(/<(script|link|img)\b/i);
    expect(body).not.toMatch(/src=|https?:\/\//i);
  });

  it("escapes the abuse URL", async () => {
    const { env } = fakeEnv({ overrides: { ABUSE_URL: 'https://example.com/report?a=1&b="x"' } });
    const body = await (await router.fetch(new Request(`https://nope.${BASE}/`), env)).text();
    expect(body).toContain('href="https://example.com/report?a=1&amp;b=&quot;x&quot;"');
  });

  it.each(["javascript:alert(1)", "data:text/html,x", "http://plain.example.com", "", undefined])(
    "leaves the link out for an abuse URL that is not https or mailto (%j)",
    async (value) => {
      const { env } = fakeEnv({ overrides: { ABUSE_URL: value } });
      const body = await (await router.fetch(new Request(`https://nope.${BASE}/`), env)).text();
      expect(body).not.toContain("<a ");
      expect(body).not.toContain("javascript:");
    },
  );
});

describe("custom hostnames (EXC-614)", () => {
  it("looks a customer's own hostname up as it is", () => {
    expect(hostKey("www.example.com", BASE)).toBe("www.example.com");
    expect(hostKey(`shop.${BASE}`, BASE)).toBe(`shop.${BASE}`);
  });

  it.each([BASE, `a.b.${BASE}`, `ab.${BASE}`, "localhost", "10.0.0.1"])("has no key for %s", (hostname) => {
    expect(hostKey(hostname, BASE)).toBeNull();
  });

  it("serves an active custom hostname from its site's Worker", async () => {
    const { env, calls } = fakeEnv({ hosts: { "www.example.com": SCRIPT } });
    const response = await router.fetch(new Request("https://www.example.com/"), env);
    expect(response.status).toBe(200);
    expect(calls.lookups[0].key).toBe("www.example.com");
    expect(calls.get[0].script).toBe(SCRIPT);
  });

  it("answers 404 for a custom hostname the platform has not routed", async () => {
    const { env, calls } = fakeEnv({ hosts: {} });
    const response = await router.fetch(new Request("https://pending.example.com/"), env);
    expect(response.status).toBe(404);
    expect(calls.get).toHaveLength(0);
  });

  it("keeps a cookie on the custom hostname it was set for", async () => {
    const site = async () => {
      const headers = new Headers();
      headers.append("set-cookie", "a=1; Domain=www.example.com");
      headers.append("set-cookie", "b=2; Domain=example.com");
      return new Response("ok", { headers });
    };
    const { env } = fakeEnv({ hosts: { "www.example.com": SCRIPT }, site });
    const response = await router.fetch(new Request("https://www.example.com/"), env);
    expect(response.headers.getSetCookie()).toEqual(["a=1; Domain=www.example.com"]);
  });
});
