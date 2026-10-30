import { describe, expect, it } from "vitest";
import router, { hostKey, previewLabel } from "../src/index.js";

// Previews (EXC-660): <site name, up to 30>--<26 lowercase base32>, which no site name can be.
const BASE = "excalibase.app";
const RANDOM = "abcdefghijklmnopqrstuvwxyz";
const PREVIEW_HOST = `shop--${RANDOM}.${BASE}`;
const PREVIEW_SCRIPT = "site-pv-0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c";

function previewEnv(site) {
  const lookups = [];
  return {
    lookups,
    env: {
      BASE_DOMAIN: BASE,
      SITE_HOSTS: {
        async get(key) {
          lookups.push(key);
          return { [PREVIEW_HOST]: PREVIEW_SCRIPT, [`shop.${BASE}`]: "site-0b6f2c9e" }[key] ?? null;
        },
      },
      SITES: { get: () => ({ fetch: site ?? (async () => new Response("<h1>preview</h1>", { headers: { "referrer-policy": "unsafe-url" } })) }) },
    },
  };
}

describe("preview labels", () => {
  it.each([`shop--${RANDOM}`, `a--${"2".repeat(26)}`, `my-shop-2--${RANDOM}`, `${"a".repeat(30)}--${RANDOM}`])("accepts %s", (label) => {
    expect(previewLabel(`${label}.${BASE}`, BASE)).toBe(label);
    expect(hostKey(`${label}.${BASE}`, BASE)).toBe(`${label}.${BASE}`);
  });

  it.each([
    [`shop--${RANDOM.slice(1)}`, "a short random part"],
    [`shop--${RANDOM}a`, "a long random part"],
    [`shop--${RANDOM.slice(0, 25)}1`, "a digit base32 has no place for"],
    [`a--b--${RANDOM}`, "a prefix that holds two hyphens"],
    [`-shop--${RANDOM}`, "a leading hyphen"],
    [`shop---${RANDOM}`, "three hyphens"],
    [`${"a".repeat(31)}--${RANDOM}`, "a prefix past 30 characters"],
    [`x.shop--${RANDOM}`, "a nested host"],
  ])("refuses %s (%s)", (label) => {
    expect(previewLabel(`${label}.${BASE}`, BASE)).toBeNull();
    expect(hostKey(`${label}.${BASE}`, BASE)).toBeNull();
  });
});

describe("preview responses", () => {
  it("routes a preview host to its own script, kept out of search and with no referrer", async () => {
    const { env, lookups } = previewEnv();
    const response = await router.fetch(new Request(`https://${PREVIEW_HOST}/`), env);
    expect(response.status).toBe(200);
    expect(lookups).toEqual([PREVIEW_HOST]);
    expect(response.headers.get("x-robots-tag")).toBe("noindex, nofollow");
    expect(response.headers.get("referrer-policy")).toBe("no-referrer");
  });

  it("leaves a live site's own referrer policy and indexing alone", async () => {
    const { env } = previewEnv();
    const response = await router.fetch(new Request(`https://shop.${BASE}/`), env);
    expect(response.headers.get("x-robots-tag")).toBeNull();
    expect(response.headers.get("referrer-policy")).toBe("unsafe-url");
  });
});
