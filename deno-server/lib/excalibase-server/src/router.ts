import type { HttpActionDef, HttpMethod, RouteDef, Router } from "./types";

/**
 * Allowed HTTP methods on `Router.route()`.
 * The constant is exported only for tests and the codegen-side scanner.
 */
export const ALLOWED_HTTP_METHODS: ReadonlySet<HttpMethod> = new Set<HttpMethod>([
  "GET",
  "POST",
  "PUT",
  "PATCH",
  "DELETE",
  "OPTIONS",
]);

const ROUTER_BRAND = Symbol.for("excalibase.httpRouter");

/**
 * Construct a new HTTP router. Users typically default-export the result
 * of `httpRouter().route(...).route(...)` from a function bundle's
 * `http.ts`; the Excalibase bundler reads the route table at deploy time
 * via the `getRoutes()` accessor and persists it on the Function record.
 *
 * The returned object is intentionally minimal — no path-parameter
 * matching, no nested routers — to keep the runtime side cheap. Path
 * matching at request time happens in the Go gateway against the
 * persisted route table.
 */
export function httpRouter(): Router {
  const routes: RouteDef[] = [];
  // Worker-side dispatcher lookup — keyed by "METHOD path" so the runtime
  // can resolve a (method, path) hit straight to its handler closure
  // without re-walking the route table on every request.
  const routeHandlers: Record<string, HttpActionDef["handler"]> = {};
  const router = {
    [ROUTER_BRAND]: true,
    // Phase 7: emit a discriminator `kind: "httpRouter"` so the Go-side
    // Bundle() scan stamps Function.Kind correctly. Without this the
    // gateway can't tell httpRouter exports apart from arbitrary v2
    // FunctionDef records.
    kind: "httpRouter" as const,
    route(def: RouteDef): Router {
      validateRouteDef(def);
      const frozen = Object.freeze({ ...def });
      routes.push(frozen);
      // Refresh the side-channel arrays / map so a bundler that
      // already read them (e.g. on hot-reload) picks up new entries.
      this.__excalibase_routes = routes.map((r) => ({
        path: r.path,
        method: r.method,
        // Phase 7 keeps the export shape flat: each route's handler is
        // inlined in the same module, so the `exportName` always
        // resolves to "default" — the bundler's persisted route table
        // uses this slot for future-proofing.
        exportName: "default",
      }));
      routeHandlers[`${def.method} ${def.path}`] = def.handler.handler;
      return router;
    },
    getRoutes(): RouteDef[] {
      return routes.slice();
    },
    // Side channel read by the Go bundler's Bundle() scan — a JSON-shaped
    // array literal of `{path, method, exportName}` rows. Populated by
    // `route()`; kept as a top-level field rather than a getter so the
    // bundler's regex sees the literal verbatim in the emitted JS.
    __excalibase_routes: [] as Array<{ path: string; method: HttpMethod; exportName: string }>,
    // Side channel read by the worker dispatcher — a map keyed by
    // "METHOD path" so an incoming request resolves in O(1) to its
    // handler closure.
    __excalibase_route_handlers: routeHandlers,
  } as Router & {
    [ROUTER_BRAND]: true;
    kind: "httpRouter";
    __excalibase_routes: Array<{ path: string; method: HttpMethod; exportName: string }>;
    __excalibase_route_handlers: Record<string, HttpActionDef["handler"]>;
  };
  return router;
}

/**
 * Structural type guard for objects produced by `httpRouter()`. The brand
 * symbol survives JSON round-trip loss (the bundler looks at the live
 * object, not a serialised copy), so a plain `{ route, getRoutes }`
 * lookalike returns `false`.
 */
export function isHttpRouter(value: unknown): value is Router {
  if (value === null || typeof value !== "object") return false;
  return (value as { [ROUTER_BRAND]?: unknown })[ROUTER_BRAND] === true;
}

function validateRouteDef(def: RouteDef): void {
  if (!def || typeof def !== "object") {
    throw new TypeError("route() requires a { path, method, handler } object");
  }
  if (typeof def.path !== "string" || def.path.length === 0 || def.path[0] !== "/") {
    throw new TypeError(`route path must be a string starting with '/' (got ${JSON.stringify(def.path)})`);
  }
  if (typeof def.method !== "string" || !ALLOWED_HTTP_METHODS.has(def.method as HttpMethod)) {
    throw new TypeError(
      `route method must be one of GET/POST/PUT/PATCH/DELETE/OPTIONS (got ${JSON.stringify(def.method)})`,
    );
  }
  if (!isHttpActionShape(def.handler)) {
    throw new TypeError(
      "route handler must be an httpAction(...) result — bare functions are not accepted",
    );
  }
}

function isHttpActionShape(value: unknown): value is HttpActionDef {
  if (value === null || typeof value !== "object") return false;
  const v = value as { kind?: unknown; handler?: unknown };
  return v.kind === "httpAction" && typeof v.handler === "function";
}
