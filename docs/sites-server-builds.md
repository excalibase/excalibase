# Server sites: building Next.js from git (EXC-661)

Behind `FEATURE_SITESSERVER` (needs `SITES_ENABLED` and git builds). **The flag
must not go on in prod until R1 passes** (below). With the flag off a server site
is as good as absent: every route answers 404 as an unmounted one, a push does
not build its connection, and a build queued before the flag went off is refused
before it runs.

A server site is created as one (Studio, New site, Kind; or `create_site` with
`kind: server`) and its kind never changes.

## What a push does

A push to the connected branch queues the same sandboxed build Job static sites
use (gVisor, plan CPU, memory, disk and time). The network fence goes up before
the clone, and outbound HTTP(S) stays open for the whole build (the registry
install needs it); only private ranges are blocked. For a server site the
connection carries `framework: "nextjs"` and the Job runs the Next.js preset
instead of `build` and an output folder:

1. install, as for any site (lockfile picks npm, pnpm or yarn);
2. if the repository does not declare `@opennextjs/cloudflare` and `wrangler`,
   the pinned versions (1.20.12 and 4.150.0) are added to the throwaway checkout.
   They have to resolve from the project, because the OpenNext config imports the
   package. A repository that declares them keeps its own versions;
3. the repository's `wrangler.jsonc/json/toml` is read. A binding in it (R2, KV,
   D1, service, Durable Object, queue, AI, unsafe, ...) refuses the build by name;
   `vars`, `routes`, `account_id` and `triggers` are ignored with a note;
4. `opennextjs-cloudflare build` (it runs your `build` script). If you set a build
   command, that runs first and OpenNext gets `--skipNextBuild`;
5. the resolved result is checked: Next's own `functions-config-manifest.json`
   (Node.js middleware) and the OpenNext config as bundled (cache, tag cache,
   queue and invalidation overrides). Reading the resolved values means a
   re-export or a helper file cannot hide an override;
6. `opennextjs-cloudflare populateCache local` (pre-rendered pages become assets);
7. `wrangler deploy --dry-run --minify --outdir` bundles the Worker into one
   module, with **our** config (`wrangler.excalibase.jsonc`), not the
   repository's. Nothing is uploaded and no Cloudflare token is involved.

The Worker is bundled and published on one compatibility date (2025-09-01) and
flag set (`nodejs_compat`). The build writes both into `site.json`, so the bundle
and the runtime cannot differ. Taking the repository's date instead was rejected:
a newer date changes the module resolution of Node built-ins, and a bundle made
for 2026-10-01 failed at startup in workerd with `No such module "node:fs"` on the
published date. A later date needs the OpenNext/workerd pair re-tested, so it is
one constant here and in the archive reader (`LatestCompatibilityDate`).

`collect` packs `server/` (the bundle, without README and source map), `assets/`
(`.open-next/assets`, under the root directory) and `site.json`, and the normal
deploy path takes it: the same archive checks, plan bundle limit, stored code for
rollback and go-live as an upload deploy. `_headers` and `_redirects` files in
`public/` are uploaded as plain files; the Worker ignores them. If the repository
has no `open-next.config.ts`, the preset writes ours (static-assets cache).

## What the code finds at runtime

Every server publish (a deploy and a rollback) binds two plain variables:
`EXCALIBASE_URL`, the platform's public API host (`PUBLIC_BASE_URL`), and
`EXCALIBASE_PROJECT_ID`. The project's API paths hang under them (the outbound
Worker lets the code reach only its own project's). Nothing else is bound: no
publishable key yet (keys are shown once, so it will be a site variable the owner
sets), and never a binding from the repository.

Runtime variables are set per site (`PUT /sites/{id}/env`, Developers) and reach
the code with the next deploy; each deploy keeps its own copy, so a rollback runs
under the variables of the deploy it restores. `EXCALIBASE_PUBLISHABLE_KEY` is the
one `EXCALIBASE_` name a site sets itself.

Project secrets reach server code only when marked server-visible
(`PUT /sites/server-secrets`, a third marking beside build-visible). They are bound
as secret text by name, read at every publish, so a rollback uses the secret's
current value. Mark one only if the repository's code should hold it: it runs in
that Worker. A secret replaces a plain variable of the same name.

## What is protection and what is a usability aid

The protection is that **no repository binding is ever sent**: provisioning
publishes the Worker with our token and our bindings only (the assets, and the
outbound Worker on the namespace). The checks above (wrangler bindings, cache
overrides, Node middleware) exist so a build that would fail or misbehave at
runtime fails early with a named reason instead. They are not the boundary.

## When it does not fit

The build fails with exit 13 (`framework` failure) and says why in its log, then
Studio shows "Deploy this repository as an app container" (app hosting). There is
no automatic switch. Reasons: Node.js middleware, a cache/queue override that
needs storage, a binding in the repository's wrangler file, or a bundle over the
plan's limit (refused by the deploy path as a `deploy` failure).

## Nuxt, SvelteKit and Astro

Chosen as the site's framework. The build runs your install and build (`npm run build`
unless you set a build command), then wrangler bundles the framework's worker
with our config, as for Next.js. Nuxt is built with `NITRO_PRESET=cloudflare_module`. SvelteKit
and Astro take their output from an adapter in your own config, which the build does
not edit: the repository must already use `@sveltejs/adapter-cloudflare` or
`@astrojs/cloudflare`, or the build is refused by name. The worker is never a public
file (`_worker.js` is kept out of the assets). Any other framework is refused when the
repository is connected, with the pointer to a container. Checked with real builds of
Nuxt 3.17, SvelteKit 2 with adapter-cloudflare 7 and Astro 5 with @astrojs/cloudflare 12; a
newer major of an adapter may write its worker elsewhere and fail with the named reason.
Astro's adapter asks for a `SESSION` KV binding when sessions are used; a site has none,
so Astro sessions do not work.

Installs without a Cloudflare account have no Sites at all (ADR 0043 decision 2);
a server-rendered frontend there is a container. Single-host installs run git
builds for sites only with `FEATURE_SINGLEHOSTBUILDS`; they run the same
`build.sh` and `collect.sh`, but the Next.js preset has not been run in the Docker
runner or a cluster Job yet. Upload deploys (`server/`, `assets/`, `site.json`)
work everywhere Sites do.

## Tested

The scripts run under bash in unit tests with stub tools that write what the real
ones write. `TestNextjsPreset_BuildsAPlainRepoWithRealTools` (integration tag,
needs Docker and the network) builds a repository that declares only `next`,
`react` and `react-dom` and has its own wrangler date with the real OpenNext and
wrangler, and feeds the output to the archive reader.

## Not covered yet

* **R1 to R7 on a real Cloudflare account (plan section 14).** Until R1 passes
  the flag stays off: it proves the OpenNext bundle runs as an untrusted namespaced
  Worker, starts in under 1 s, and that `/cdn-cgi/_next_cache/...` is not public.
  R4 (size limits), R5 (per-call limits, outbound parameters, `connect()` blocked)
  and R6 (rollback by re-upload) are also open; R2, R3 and R7 are in the plan.
* **Pre-rendered pages are public assets** under `/cdn-cgi/_next_cache/<build id>/`.
  Only Cloudflare's reserved path was keeping them private, which is unverified in
  a dispatch namespace, so the sites router now answers that prefix 404 for server
  sites in any spelling. `/BUILD_ID` is still a public file (the build id is not a
  secret).
* a local run of the bundle in workerd before it is published (the spike found
  Next/OpenNext version pairs that build green and fail every dynamic route);
* a Next/OpenNext version matrix beyond the single tested pair, and the pnpm and
  yarn paths of adding the tools (only npm is run for real);
* build cache, other frameworks, ISR, runtime variables and server-visible
  secrets (ADR 0045 steps 3, 6, 9, 10).
