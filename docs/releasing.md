# Releasing

A release promotes images that were already tested; nothing is rebuilt.

1. **No release tag without a green user-journey gate.** Run *User journeys (release gate)*
   (`.github/workflows/user-journeys.yml`) from `main` on the tag you will promote, e.g.
   `main-<sha>`, or use the nightly run that already tested it. Runs from other branches never
   count. All three legs must pass: single host on Docker, single
   host on Podman, and platform-aio on k3d. Each leg builds, deploys and walks the example apps
   (lecture-search, live-sports-dashboard, nestjs-api bookshelf) the way their builders do.
2. The AIO nightly in `excalibase-service` must be green for the same images.
3. Run *Release* (`.github/workflows/release.yml`) with the version and that source tag. Its
   `gate` job refuses unless a green gate run on `main` tested every image digest it would
   promote, and retag promotes exactly those digests.
4. Tag the commit `vX.Y.Z` and publish the GitHub Release. Production moves only to a release.

To run a gate leg locally (it waits for the heavy-suite lock and destroys its own stack):

```bash
tests/user-journeys/run.sh docker   # or podman, or k3d with SERVICE_DIR=<excalibase-service>
```

`TAG` picks the published images (default `main`); they are resolved to digests once and run by
digest. `PLATFORM_TAG` runs provisioning and Studio built locally from a branch.
`ONLY=lecture-search` walks one example. `HEAVY_LOCK` names the lock file shared with other heavy
suites (default `$TMPDIR/excalibase-heavy.lock`). The Docker leg refuses to start while this
machine has another single host's volumes or projects: the bundle's volume names are fixed.

The single-host legs report the live-goal journey of live-sports as a known gap
(`E2E_KNOWN_GAPS=realtime`): a single host runs no CDC watcher yet, so subscriptions deliver no
table changes there. The k3d leg runs it.

CI reads the private example repos and `excalibase-service` with read-only deploy keys:
`EXAMPLE_LECTURE_SEARCH_DEPLOY_KEY`, `EXAMPLE_LIVE_SPORTS_DEPLOY_KEY`,
`EXAMPLE_NESTJS_API_DEPLOY_KEY` and `EXCALIBASE_SERVICE_DEPLOY_KEY`.
