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

Rules that keep this safe:

- `release-<major>.<minor>-<sha7>` is written once. CD refuses to push over an
  existing one (`scripts/guard-release-tag.sh`), so a re-run cannot replace a
  tested image with an untested digest. `main-<sha7>` is not guarded and can be
  overwritten by a re-run, as before.
- The floating `release-1.7` (and `main`) moves only while the built commit is
  still the branch tip, so a late run cannot move it backwards.

## Patch release (for example 1.7.1)

1. **Bring CI and CD to the branch.** A push runs the `ci.yml` of the branch
   it lands on, so a `release/*` branch cut before #549, or from a tag older
   than it, gets no CI and no CD until it carries the new `.github/workflows/ci.yml`
   and `tests/cd-plan-test.sh` (plus `tests/cd-guard-test.sh`,
   `scripts/guard-release-tag.sh`, `scripts/release-tags.sh` and the updated
   `scripts/release-native-tags.sh`). Copy them from `main` as the first commit
   on the branch. Do this once per existing release branch.
2. **Cut the branch** once per minor, from the last release tag, in each repo
   that needs a fix: `git switch -c release/1.7 v1.7.0 && git push origin release/1.7`.
   The name must be `release/<major>.<minor>` without leading zeros; any other
   `release/*` name is refused by CD.
3. **Cherry-pick** the fixes, merged to `main` first, as pull requests into
   `release/1.7` (CI runs on them). Bump the single-host bundle tag on the
   branch if the bundle changed.
4. **CD builds the release images.** The push to `release/1.7` runs CI, then
   `cd.yml` publishes `excalibase/provisioning`, `studio` and `deno-runtime`
   as `release-1.7-<sha7>`, Trivy-scanned before the push. `main` can be any
   distance ahead. Images owned by other repos (engine, auth, watcher) are
   built by their own CD; see the note at the end.
5. **Gate it.** In `excalibase-service`, dispatch *AIO E2E (k3d)* **from the
   matching `release-chart-1.7.x` branch**, not from `main`: the run installs the
   chart of the ref it is dispatched from, and the gate must test the chart that
   ships. That branch needs the dispatch inputs from excalibase-service#214 (cherry-pick
   `aio-e2e.yml` and `aio-e2e/lib/resolve-images.*` onto it first). Set
   `tag=release-1.7-<sha7>`, and per-image overrides (`provisioning`, `studio`,
   `graphql`, `auth`, `watcher`, `deno`) for any image not built from the
   branch, for example `graphql=excalibase/excalibase-graphql:1.7.0@sha256:...`.
   No throwaway branch is needed. Wait for green.
6. **Retag.** Dispatch *Release* from `release/1.7` with `version=1.7.1`,
   `source_tag=release-1.7-<sha7>` and
   `services=provisioning,studio,deno-runtime` (the images built from the
   branch). It resolves the tested digest, scans it, and adds `1.7.1`, `1.7`,
   `1` and `latest`. When 1.7 is **not** the newest minor (1.8.0 already
   shipped), set `move_major_latest=false`: only `1.7.1` and `1.7` move, so `1`
   and `latest` stay on 1.8.
7. **Retag the images that were not patched.** The chart bump below rewrites
   every excalibase image to `:1.7.1`, so each of the six needs that tag, patched
   or not. Dispatch *Release* a second time from `release/1.7` with
   `version=1.7.1`, `source_tag=<previous release, 1.7.0>`,
   `services=excalibase-graphql,excalibase-auth,excalibase-watcher-go`
   (whichever were not rebuilt) and the same `move_major_latest`. Use the
   digests the gate installed, so check that step 5 used these same refs.
8. **Chart bump.** In `excalibase-service`, publish the GitHub release `v1.7.1`;
   `chart-release.yml` packages the chart with every image at `1.7.1`
   (`scripts/package-chart.sh`).
9. **Upgrade production.** Follow
   [section 4.1 of the production runbook](deployment/production-k8s-runbook.md)
   (`helm upgrade` re-runs the idempotent bootstrap Job); the runbook's example
   shows `main-<sha>` tags, so use the `1.7.1` digests the gate installed
   instead. Production runs the release branch's images, not `main`.

Engine (`excalibase-graphql`) and auth have their own CD, which is still
main-only. Until they publish `release-<major>.<minor>-<sha7>` too, a patch to
those images needs their CD extended the same way; unpatched ones are retagged
in step 7.
