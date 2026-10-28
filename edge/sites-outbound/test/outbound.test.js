import { describe, expect, it, vi } from "vitest";
import outbound, { blockedAddress } from "../src/index.js";

const PROJECT = "proj-shop1";
const API = "api.excalibase.io";

// fakeEnv is what the outbound Worker sees: its own vars from deploy.sh, and
// the site's identity the router passed for this call.
function fakeEnv(overrides = {}) {
  return {
    API_HOST: API,
    PLATFORM_HOSTS: ".excalibase.io,148.113.0.42",
    siteId: "0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c",
    projectId: PROJECT,
    ...overrides,
  };
}

function withUpstream(answer = () => new Response("upstream")) {
  const sent = [];
  const fetchMock = vi.fn(async (request, init) => {
    sent.push({ request, init });
    return answer(request);
  });
  vi.stubGlobal("fetch", fetchMock);
  return sent;
}

async function call(url, init, env = fakeEnv()) {
  return outbound.fetch(new Request(url, init), env);
}

describe("the project's own API", () => {
  it.each([
    `https://${API}/${PROJECT}/graphql`,
    `https://${API}/${PROJECT}/api/v1/todos?limit=5`,
    `https://${API}/${PROJECT}/graphql/`,
    `https://${API}/auth/${PROJECT}/${PROJECT}/token`,
    `https://${API}/functions/v1/${PROJECT}/hello`,
    `https://${API}/storage/v1/${PROJECT}/buckets/files/objects`,
  ])("is reached: %s", async (url) => {
    const sent = withUpstream();
    const response = await call(url);
    expect(response.status).toBe(200);
    expect(sent).toHaveLength(1);
    expect(sent[0].request.url).toBe(url);
  });

  it("is reached without headers in the platform's own namespace", async () => {
    const sent = withUpstream();
    await call(`https://${API}/${PROJECT}/graphql`, { method: "POST", headers: { "x-excalibase-project": "other", "x-excalibase-publishable-key": "pk_live", authorization: "Bearer end-user" }, body: "{}" });
    expect(sent[0].request.headers.get("x-excalibase-project")).toBeNull();
    expect(sent[0].request.headers.get("authorization")).toBe("Bearer end-user");
    expect(sent[0].request.headers.get("x-excalibase-publishable-key")).toBe("pk_live");
    expect(await sent[0].request.text()).toBe("{}");
  });

  it.each([
    `https://${API}/proj-other/graphql`,
    `https://${API}/auth/proj-other/x/token`,
    `https://${API}/functions/v1/proj-other/hello`,
    `https://${API}/storage/v1/proj-other/buckets`,
    `https://${API}/api/sites/github/webhook`,
    `https://${API}/${PROJECT}/../proj-other/graphql`,
    `https://${API}/%70roj-shop1/graphql`,
    `https://${API}/`,
    `https://${API}/${PROJECT}`,
    `https://${API}/${PROJECT}/admin`,
  ])("refuses another project's or the platform's path: %s", async (url) => {
    const sent = withUpstream();
    const response = await call(url);
    expect(response.status).toBe(403);
    expect(sent).toHaveLength(0);
  });

  it("refuses every API path when the router passed no project", async () => {
    const sent = withUpstream();
    const response = await call(`https://${API}/${PROJECT}/graphql`, undefined, fakeEnv({ projectId: undefined }));
    expect(response.status).toBe(403);
    expect(sent).toHaveLength(0);
  });
});

describe("platform hosts", () => {
  it.each([
    "https://studio.excalibase.io/api/projects",
    "https://admin.excalibase.io/api/auth/tokens",
    "https://excalibase.io/",
    "https://STUDIO.Excalibase.io/",
    "https://148.113.0.42/api",
  ])("are refused: %s", async (url) => {
    const sent = withUpstream();
    expect((await call(url)).status).toBe(403);
    expect(sent).toHaveLength(0);
  });

  it("does not take a lookalike for a platform host", async () => {
    const sent = withUpstream();
    expect((await call("https://notexcalibase.io/")).status).toBe(200);
    expect(sent).toHaveLength(1);
  });
});

describe("addresses", () => {
  it.each([
    "http://127.0.0.1/", "http://10.1.2.3/", "http://172.16.0.1/", "http://192.168.1.1/", "http://169.254.169.254/latest/meta-data",
    "http://100.64.0.1/", "http://0.0.0.0/", "http://[::1]/", "http://[fd00::1]/", "http://[fe80::1]/", "http://[::ffff:10.0.0.1]/",
    "http://[64:ff9b::a00:1]/", "http://[2001:db8::1]/", "http://2130706433/", "http://0x7f.1/", "http://localhost/", "http://api.localhost/",
    "http://metadata.google.internal/", "http://printer.local/", "http://224.0.0.1/", "http://198.18.0.1/",
  ])("in private, local and reserved ranges are refused: %s", async (url) => {
    const sent = withUpstream();
    expect((await call(url)).status).toBe(403);
    expect(sent).toHaveLength(0);
  });

  it.each(["8.8.8.8", "[2606:4700::1111]", "example.com"])("on the public internet are reached: %s", (host) => {
    expect(blockedAddress(new URL(`https://${host}/`).hostname)).toBe(false);
  });
});

describe("the public internet", () => {
  it("is reached with the request as the code sent it", async () => {
    const sent = withUpstream();
    const response = await call("https://example.com/data", { method: "PUT", headers: { "x-api-key": "k" }, body: "b" });
    expect(response.status).toBe(200);
    expect(sent[0].request.method).toBe("PUT");
    expect(sent[0].request.headers.get("x-api-key")).toBe("k");
  });

  it("refuses schemes other than http and https", async () => {
    const sent = withUpstream();
    expect((await call("ftp://example.com/")).status).toBe(403);
    expect(sent).toHaveLength(0);
  });

  it("checks every hop of a redirect it follows", async () => {
    const sent = withUpstream((request) =>
      request.url === "https://example.com/go"
        ? new Response(null, { status: 302, headers: { location: "https://studio.excalibase.io/api/projects" } })
        : new Response("should not be reached"));
    const response = await call("https://example.com/go");
    expect(response.status).toBe(403);
    expect(sent.map((s) => s.request.url)).toEqual(["https://example.com/go"]);
  });

  it("follows a redirect to an allowed host, turning a 303 into a GET", async () => {
    const sent = withUpstream((request) =>
      request.url === "https://example.com/form"
        ? new Response(null, { status: 303, headers: { location: "/done" } })
        : new Response("done"));
    const response = await call("https://example.com/form", { method: "POST", body: "a=1" });
    expect(await response.text()).toBe("done");
    expect(sent[1].request.url).toBe("https://example.com/done");
    expect(sent[1].request.method).toBe("GET");
  });

  it("hands a redirect back when the code asked for that", async () => {
    withUpstream(() => new Response(null, { status: 302, headers: { location: "https://studio.excalibase.io/" } }));
    const response = await call("https://example.com/go", { redirect: "manual" });
    expect(response.status).toBe(302);
  });

  it("stops after too many redirects", async () => {
    const sent = withUpstream((request) => new Response(null, { status: 302, headers: { location: request.url + "x" } }));
    const response = await call("https://example.com/loop");
    expect(response.status).toBe(508);
    expect(sent.length).toBe(6);
  });

  it("fails closed when PLATFORM_HOSTS or API_HOST is not bound", async () => {
    const sent = withUpstream();
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    expect((await call("https://example.com/", undefined, fakeEnv({ PLATFORM_HOSTS: undefined }))).status).toBe(503);
    expect((await call("https://example.com/", undefined, fakeEnv({ API_HOST: "" }))).status).toBe(503);
    expect(sent).toHaveLength(0);
    errors.mockRestore();
  });
});

describe("hostnames written another way", () => {
  it.each([
    `https://${API}./proj-other/graphql`,
    "https://studio.excalibase.io./api/projects",
    "https://studio.excalibase.io../",
    "http://localhost./",
    "http://metadata.google.internal./",
  ])("are refused like the name they stand for: %s", async (url) => {
    const sent = withUpstream();
    expect((await call(url)).status).toBe(403);
    expect(sent).toHaveLength(0);
  });

  it("a trailing dot on the API host still strips the platform's headers", async () => {
    const sent = withUpstream();
    await call(`https://${API}./${PROJECT}/graphql`, { headers: { "x-excalibase-project": "other" } });
    expect(sent[0].request.headers.get("x-excalibase-project")).toBeNull();
  });
});

describe("the project's API paths", () => {
  it.each([
    `https://${API}/${PROJECT}/graphql/..%2F..%2Fproj-other/graphql`,
    `https://${API}/${PROJECT}/graphql/%2e%2e/x`,
    `https://${API}/${PROJECT}/api//v1`,
    `https://${API}/${PROJECT}/api/v1%5c..%5cx`,
    `https://${API}/${PROJECT}/graphql/..;/proj-other/graphql`,
    `https://${API}/${PROJECT}/graphql;x`,
  ])("refuse encoded separators, path parameters and empty segments: %s", async (url) => {
    const sent = withUpstream();
    expect((await call(url)).status).toBe(403);
    expect(sent).toHaveLength(0);
  });

  it("drop forwarding headers the code could use to pass as someone else", async () => {
    const sent = withUpstream();
    await call(`https://${API}/${PROJECT}/graphql`, {
      headers: { "x-forwarded-for": "10.0.0.1", forwarded: "for=10.0.0.1", "x-real-ip": "10.0.0.1", "true-client-ip": "1.1.1.1", "x-forwarded-host": "studio.excalibase.io" },
    });
    for (const name of ["x-forwarded-for", "forwarded", "x-real-ip", "true-client-ip", "x-forwarded-host"]) {
      expect(sent[0].request.headers.get(name)).toBeNull();
    }
  });
});

describe("a redirect to another origin", () => {
  it("does not carry the code's credentials there", async () => {
    const sent = withUpstream((request) =>
      request.url === "https://example.com/go"
        ? new Response(null, { status: 302, headers: { location: "https://elsewhere.example.org/" } })
        : new Response("there"));
    await call("https://example.com/go", { headers: { authorization: "Bearer t", cookie: "s=1", "proxy-authorization": "x", "x-api-key": "k", apikey: "k", accept: "text/html" } });
    const hop = sent[1].request.headers;
    expect(hop.get("authorization")).toBeNull();
    expect(hop.get("cookie")).toBeNull();
    expect(hop.get("proxy-authorization")).toBeNull();
    expect(hop.get("x-api-key")).toBeNull();
    expect(hop.get("apikey")).toBeNull();
    expect(hop.get("accept")).toBe("text/html");
  });

  it("keeps them on the same origin", async () => {
    const sent = withUpstream((request) =>
      request.url === "https://example.com/go" ? new Response(null, { status: 307, headers: { location: "/next" } }) : new Response("ok"));
    await call("https://example.com/go", { headers: { authorization: "Bearer t" } });
    expect(sent[1].request.headers.get("authorization")).toBe("Bearer t");
  });
});

describe("refusals", () => {
  it("are logged with the site that asked", async () => {
    withUpstream();
    const logs = vi.spyOn(console, "log").mockImplementation(() => {});
    await call("https://studio.excalibase.io/");
    expect(JSON.parse(logs.mock.calls[0][0])).toMatchObject({ refused: "platform", site: "0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c", host: "studio.excalibase.io" });
    logs.mockRestore();
  });
});

describe("IPv6 platform addresses", () => {
  it("are refused however they are written", async () => {
    const sent = withUpstream();
    const env = fakeEnv({ PLATFORM_HOSTS: ".excalibase.io,2001:41d0:304:200::5e0" });
    expect((await call("https://[2001:41d0:304:200:0:0:0:5e0]/", undefined, env)).status).toBe(403);
    expect((await call("https://[2001:41D0:304:200::5E0]/", undefined, env)).status).toBe(403);
    expect(sent).toHaveLength(0);
  });
});

describe("the request sent on", () => {
  it("carries only the method, headers and body the code gave", async () => {
    const sent = withUpstream();
    await call("https://example.com/x", { method: "POST", headers: { "x-a": "1" }, body: "b" });
    const { request, init } = sent[0];
    expect(init).toEqual({ redirect: "manual" });
    expect(request.method).toBe("POST");
    expect(await request.text()).toBe("b");
  });
});
