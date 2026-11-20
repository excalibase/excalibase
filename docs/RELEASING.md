# Releasing

Images are built once, tested as built, then retagged. Nothing is rebuilt for a
release.

| Tag | Made by | Meaning |
|---|---|---|
| `main`, `main-<sha7>` | `cd.yml`, after CI passes on `main` | trunk |
| `release-1.7`, `release-1.7-<sha7>` | `cd.yml`, after CI passes on `release/1.7` | a release branch; `release-1.7` floats, `-<sha7>` does not |
| `1.7.1`, `1.7`, `1`, `latest` | `release.yml` (retag) | a release; `latest` only for a non-prerelease |

`cd.yml` never writes a version tag or `latest`, and a release branch never
touches a `main` tag.

## Patch release (for example 1.7.1)

1. **Cut the branch** once per minor, from the last release tag, in each repo
   that needs a fix: `git switch -c release/1.7 v1.7.0 && git push origin release/1.7`.
   The name must be `release/<major>.<minor>`; any other `release/*` name is
   refused by CD.
2. **Cherry-pick** the fixes, merged to `main` first, as pull requests into
   `release/1.7` (CI runs on them). Bump the single-host bundle tag on the
   branch if the bundle changed.
3. **CD builds the release images.** The push to `release/1.7` runs CI, then
   `cd.yml` publishes `excalibase/provisioning`, `studio` and `deno-runtime`
   as `release-1.7-<sha7>`, Trivy-scanned before the push. `main` can be any
   distance ahead. Images owned by other repos (engine, auth, watcher) are
   built by their own CD; see the note below.
4. **Gate it.** In `excalibase-service`, dispatch *AIO E2E (k3d)* with
   `tag=release-1.7-<sha7>`, or a digest per image (`provisioning`, `studio`,
   `graphql`, `auth`, `watcher`, `deno`) for any image that is not built from
   the branch, such as `graphql=excalibase/excalibase-graphql:1.7.0@sha256:...`.
   No throwaway branch is needed. Wait for green.
5. **Retag.** Dispatch *Release* from `release/1.7` with `version=1.7.1`,
   `source_tag=release-1.7-<sha7>` and `services` limited to the images built
   from the branch (default lists the engine and auth too; leave them out if
   they were not patched). It resolves the tested digest, scans it, and adds
   `1.7.1`, `1.7`, `1` and `latest`.
6. **Chart bump.** In `excalibase-service`, publish the GitHub release `v1.7.1`;
   `chart-release.yml` packages the chart with every image at `1.7.1`
   (`scripts/package-chart.sh`).
7. **Prod upgrade kit.** Upgrade production by the digests the gate installed,
   following [section 4.1 of the production runbook](deployment/production-k8s-runbook.md)
   (`helm upgrade` re-runs the idempotent bootstrap Job). Production runs the
   release branch's images, not `main`.

Engine (`excalibase-graphql`) and auth have their own CD. Until they publish
`release-<major>.<minor>-<sha7>` too, their patch needs the engine repo's CD
extended the same way.
