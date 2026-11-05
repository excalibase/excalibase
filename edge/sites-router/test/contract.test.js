import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import router, { hostEntry } from "../src/index.js";

// The platform's SITE_HOSTS values, written by server-go's
// TestRouteEntriesMatchTheRouterContract. If either side changes the shape,
// one of the two suites fails.
const FIXTURES = new URL("../../../server-go/internal/sites/testdata/", import.meta.url);
const fixture = (name) => readFileSync(new URL(name, FIXTURES), "utf8");
const BASE = "excalibase.app";

function envFor(value) {
  const calls = [];
  const env = {
    BASE_DOMAIN: BASE,
    OUTBOUND: "on",
    SITE_HOSTS: { async get() { return value; } },
    SITES: {
      get(script, args, options) {
        calls.push({ script, options });
        return { fetch: async () => new Response("ok", { headers: { "cf-worker": "x", "x-excalibase-project": "y" } }) };
      },
    },
  };
  return { env, calls };
}

describe("the platform's SITE_HOSTS entries", () => {
  it("reads a server site's entry with its project, limits and pause flag", () => {
    expect(hostEntry(fixture("router-entry-server.json"))).toEqual({
      script: "site-0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c",
      project: "proj-shop1",
      paused: false,
      limits: { cpuMs: 50, subRequests: 20 },
    });
  });

  it("runs a server site behind the outbound Worker with its project, and strips reserved headers", async () => {
    const { env, calls } = envFor(fixture("router-entry-server.json"));
    const response = await router.fetch(new Request(`https://ssr.${BASE}/`), env);
    expect(calls[0].options.outbound).toEqual({ siteId: "0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c", projectId: "proj-shop1" });
    expect(response.headers.get("cf-worker")).toBeNull();
    expect(response.headers.get("x-excalibase-project")).toBeNull();
  });

  it("reads a static site's entry as its script name", () => {
    expect(hostEntry(fixture("router-entry-static.txt"))).toEqual({ script: "site-0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c" });
  });
});
