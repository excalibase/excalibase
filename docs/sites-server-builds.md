# Server sites: building Next.js from git (EXC-661)

Behind `FEATURE_SITESSERVER` (needs `SITES_ENABLED` and git builds). Off in prod
until the owner turns it on. A server site is created as one (Studio, New site,
Kind) and its kind never changes.

## What a push does

A push to the connected branch queues the same sandboxed build Job static sites
use (gVisor, egress fenced after install, plan CPU, memory, disk and time). For a
server site the connection carries `framework: "nextjs"` and the Job runs the
Next.js preset instead of `build` + an output folder:

1. install, as for any site (lockfile picks npm, pnpm or yarn);
2. `opennextjs-cloudflare build` (it runs your `build` script). If you set a build
   command, that runs first and OpenNext gets `--skipNextBuild`;
3. `opennextjs-cloudflare populateCache local` (pre-rendered pages become assets);
4. `wrangler deploy --dry-run --minify --outdir` bundles the Worker into one
   module. Nothing is uploaded and no Cloudflare token is involved.

`collect` packs `server/` (the bundle, without the source map), `assets/`
(`.open-next/assets`) and `site.json`, then the normal deploy path takes it: the
same archive checks, plan bundle limit, stored code for rollback and go-live as
an upload deploy. If the repository has no `open-next.config.ts` or wrangler
file, the preset writes ours (static-assets cache). Tools come from your
`node_modules` when present; otherwise the pinned versions are fetched
(OpenNext 1.20.12, wrangler 4.150.0).

## When it does not fit

The build fails with exit 13 (`framework` failure) and says why in its log, then
Studio shows "Deploy this repository as an app container" (app hosting). There is
no automatic switch. The preset refuses:

* Node.js middleware (`proxy.ts`, or `middleware.ts` with the nodejs runtime);
* an `open-next.config.ts` that names an R2, KV, D1 or Durable Object cache or
  queue (a binding would reach storage that is not the site's);
* a bundle over the plan's limit (refused by the deploy path, `deploy` failure).

Any other framework is refused when the repository is connected, with the same
pointer to a container (Nuxt, SvelteKit, Astro: planned).

Installs without a Cloudflare account have no Sites at all (ADR 0043 decision 2);
a server-rendered frontend there is a container. Single-host installs run git
builds for sites only with `FEATURE_SINGLEHOSTBUILDS`; they run the same
`build.sh` and `collect.sh`, but the Next.js preset has only run under script tests and a local
container, not yet in a cluster Job or the Docker runner. Upload deploys (`server/`, `assets/`,
`site.json`) work everywhere Sites do.

## Not covered yet

* a local run of the bundle in workerd before it is published (the spike found
  Next/OpenNext version pairs that build green and fail every dynamic route);
* a Next/OpenNext version matrix beyond the single tested pair;
* build cache, other frameworks, ISR, runtime variables and server-visible
  secrets (ADR 0045 steps 3, 6, 9, 10).
