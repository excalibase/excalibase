import { describe, expect, it, vi } from "vitest";
import router, { hostEntry } from "../src/index.js";

// Server-rendered sites (EXC-661, ADR 0045): the platform records a JSON
// entry for the hostname, and the router passes its limits and the site's
// identity to Workers for Platforms on every call.
const BASE = "excalibase.app";
const SITE_ID = "0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c";
const SCRIPT = `site-${SITE_ID}`;
const PROJECT = "proj-shop1";

function serverEntry(fields = {}) {
  return JSON.stringify({ script: SCRIPT, project: PROJECT, cpuMs: 50, subRequests: 20, ...fields });
}

function fakeEnv({ entry = serverEntry(), site, overrides = {} } = {}) {
  const calls = { get: [], fetch: [] };
  const siteFetch = site ?? (async () => new Response("<h1>ssr</h1>", { headers: { "content-type": "text/html" } }));
  const env = {
    BASE_DOMAIN: BASE,
    ABUSE_URL: "mailto:abuse@example.com",
    OUTBOUND: "on",
    SITE_HOSTS: { async get(key) { return key === `ssr.${BASE}` ? entry : null; } },
    SITES: {
      get(script, args, options) {
        calls.get.push({ script, args, options });
        return {
          async fetch(request) {
            calls.fetch.push(request);
            return siteFetch(request);
          },
        };
      },
    },
    ...overrides,
  };
  return { env, calls };
}

const get = (env, path = "/") => router.fetch(new Request(`https://ssr.${BASE}${path}`), env);

describe("a server site's entry", () => {
  it("passes the plan's limits and the site's identity on the call", async () => {
    const { env, calls } = fakeEnv();
    const response = await get(env);
    expect(response.status).toBe(200);
    expect(calls.get).toEqual([{
      script: SCRIPT,
      args: {},
      options: { limits: { cpuMs: 50, subRequests: 20 }, outbound: { siteId: SITE_ID, projectId: PROJECT } },
    }]);
  });

  it("passes no outbound parameters while no outbound Worker is bound", async () => {
    const { env, calls } = fakeEnv({ overrides: { OUTBOUND: undefined } });
    await get(env);
    expect(calls.get[0].options).toEqual({ limits: { cpuMs: 50, subRequests: 20 } });
  });

  it.each([
    ["not JSON", "{script:"],
    ["no project", JSON.stringify({ script: SCRIPT, cpuMs: 50, subRequests: 20 })],
    ["a script that is not a site's", serverEntry({ script: "excalibase-sites-router" })],
    ["a project id with a slash", serverEntry({ project: "a/b" })],
    ["no CPU limit", serverEntry({ cpuMs: undefined })],
    ["a CPU limit of zero", serverEntry({ cpuMs: 0 })],
    ["a CPU limit past Cloudflare's", serverEntry({ cpuMs: 300001 })],
    ["a fractional CPU limit", serverEntry({ cpuMs: 1.5 })],
    ["a subrequest limit as text", serverEntry({ subRequests: "20" })],
    ["a subrequest limit past Cloudflare's", serverEntry({ subRequests: 10001 })],
    ["a paused flag that is not a boolean", serverEntry({ paused: "yes" })],
    ["an array", JSON.stringify([SCRIPT])],
  ])("is refused, never run unlimited, when it has %s", async (_, entry) => {
    const { env, calls } = fakeEnv({ entry });
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const response = await get(env);
    expect(response.status).toBe(404);
    expect(calls.get).toEqual([]);
    expect(errors).toHaveBeenCalled();
    errors.mockRestore();
  });

  it("answers a paused site with the pause page and never runs its code", async () => {
    const { env, calls } = fakeEnv({ entry: serverEntry({ paused: true }) });
    const response = await get(env);
    expect(response.status).toBe(503);
    expect(response.headers.get("cache-control")).toBe("no-store");
    expect(await response.text()).toContain("paused");
    expect(calls.get).toEqual([]);
  });

  it("still reads a plain script name as a static site", () => {
    expect(hostEntry(SCRIPT)).toEqual({ script: SCRIPT });
  });
});

describe("a server site's response", () => {
  it("loses headers in the platform's and Cloudflare's namespaces, which its code may not set", async () => {
    const { env } = fakeEnv({
      site: async () => new Response("ok", {
        headers: { "x-excalibase-project": "other", "CF-Cache-Status": "HIT", "cf-worker": "x", "x-powered-by": "next" },
      }),
    });
    const response = await get(env);
    expect(response.headers.get("x-excalibase-project")).toBeNull();
    expect(response.headers.get("cf-cache-status")).toBeNull();
    expect(response.headers.get("cf-worker")).toBeNull();
    expect(response.headers.get("x-powered-by")).toBe("next");
  });

  it("keeps the cookie rules of every site", async () => {
    const headers = new Headers();
    headers.append("set-cookie", "session=1; Path=/; HttpOnly");
    headers.append("set-cookie", `wide=2; Domain=${BASE}`);
    const { env } = fakeEnv({ site: async () => new Response("ok", { headers }) });
    const response = await get(env);
    expect(response.headers.getSetCookie()).toEqual(["session=1; Path=/; HttpOnly"]);
  });

  it("answers 502 without the cause when its code exceeds a limit", async () => {
    const { env } = fakeEnv({ site: async () => { throw new Error("Worker exceeded CPU time limit."); } });
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const response = await get(env);
    expect(response.status).toBe(502);
    expect(await response.text()).not.toContain("CPU");
    errors.mockRestore();
  });
});
