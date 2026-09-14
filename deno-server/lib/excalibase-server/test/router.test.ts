import { httpAction, httpRouter, isHttpRouter } from "../src";

describe("httpRouter()", () => {
  it("returns a Router object with .route() and .getRoutes()", () => {
    const router = httpRouter();
    expect(typeof router.route).toBe("function");
    expect(typeof router.getRoutes).toBe("function");
    expect(router.getRoutes()).toEqual([]);
  });

  it("isHttpRouter recognises a Router instance and rejects non-routers", () => {
    expect(isHttpRouter(httpRouter())).toBe(true);
    expect(isHttpRouter(null)).toBe(false);
    expect(isHttpRouter(undefined)).toBe(false);
    expect(isHttpRouter({})).toBe(false);
    expect(isHttpRouter({ kind: "httpRouter" })).toBe(false);
  });

  it("route() registers a method/path/handler triple and returns the router (chainable)", () => {
    const router = httpRouter();
    const handler = httpAction(async () => new Response("ok"));
    const ret = router.route({ path: "/hello", method: "GET", handler });
    expect(ret).toBe(router);
    const routes = router.getRoutes();
    expect(routes).toHaveLength(1);
    expect(routes[0]).toEqual({ path: "/hello", method: "GET", handler });
  });

  it("supports the documented HTTP methods", () => {
    const router = httpRouter();
    const handler = httpAction(async () => new Response("ok"));
    router.route({ path: "/g", method: "GET", handler });
    router.route({ path: "/p", method: "POST", handler });
    router.route({ path: "/u", method: "PUT", handler });
    router.route({ path: "/d", method: "DELETE", handler });
    router.route({ path: "/pa", method: "PATCH", handler });
    router.route({ path: "/o", method: "OPTIONS", handler });
    const methods = router.getRoutes().map((r) => r.method).sort();
    expect(methods).toEqual(["DELETE", "GET", "OPTIONS", "PATCH", "POST", "PUT"]);
  });

  it("rejects a handler that isn't an httpAction", () => {
    const router = httpRouter();
    expect(() =>
      router.route({
        path: "/x",
        method: "GET",
        // @ts-expect-error — handler must be an httpAction
        handler: async () => new Response("nope"),
      }),
    ).toThrow(/httpAction/i);
  });

  it("rejects a path that doesn't start with '/'", () => {
    const router = httpRouter();
    const handler = httpAction(async () => new Response("ok"));
    expect(() => router.route({ path: "noslash", method: "GET", handler })).toThrow(/path/i);
  });

  it("rejects an unsupported method", () => {
    const router = httpRouter();
    const handler = httpAction(async () => new Response("ok"));
    expect(() =>
      router.route({
        path: "/x",
        // @ts-expect-error — method must be one of the supported HTTP verbs
        method: "BREW",
        handler,
      }),
    ).toThrow(/method/i);
  });

  it("getRoutes() returns a defensive copy — callers cannot mutate the internal list", () => {
    const router = httpRouter();
    const handler = httpAction(async () => new Response("ok"));
    router.route({ path: "/a", method: "GET", handler });
    const routes = router.getRoutes();
    (routes as unknown as unknown[]).push("evil");
    expect(router.getRoutes()).toHaveLength(1);
  });
});
