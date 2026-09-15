# Functions: replay on runtime cold start

The per-project Deno runtime keeps deployed functions in memory only. When its
pod restarts (eviction, node drain, OOM, image roll-out, scale-to-zero) it comes
back empty and every invoke returns "function not found" until something
re-deploys. Provisioning closes that gap automatically: the control plane is the
source of truth, and it pushes the project's functions back whenever it sees a
cold runtime (EXC-337).

## How a restart is detected

Every runtime mints a random `bootId` at process start and reports it on
`GET /health` next to the number of loaded scripts:

```json
{ "status": "healthy", "scripts": 3, "uptime": 91234.5, "bootId": "8f1c…" }
```

Provisioning runs one replay loop for the whole platform. Every poll interval it
lists the projects that own at least one function (`edge_functions` in the
platform store, or the `STORAGE_PATH` layout when self-hosted) and probes each
project's runtime. A project is replayed when:

* the runtime is seen for the first time **and** reports zero loaded scripts
  (cold runtime, or provisioning itself just restarted next to a cold runtime);
* the reported `bootId` differs from the one recorded on the previous poll
  (the runtime restarted between two polls);
* a runtime that predates the `bootId` field, and that previously held
  functions, now reports zero scripts.

A runtime that is unreachable (pod still starting, not yet provisioned) is
skipped until it answers; the loop never creates runtimes, it only repairs
existing ones. A warm runtime seen for the first time is left alone.

## What is replayed

Exactly what the first deploy sent: each function is re-bundled from its stored
source together with the project's `_shared/` modules, the schema preamble is
re-attached for functions that call `defineSchema`, and the environment is
rebuilt from the platform builtins plus the project's secrets. Secret-triggered
redeploys use the same code path, so the three deploy flows ship identical
bundles.

Replays are idempotent: `POST /deploy` replaces a function in place, so
replaying a runtime that already holds a function is harmless.

## Rate limiting and retries

* **One replay per project per 30 s.** A runtime that flaps (crash-looping pod)
  is replayed at most once per window; restarts inside the window collapse into
  a single deferred replay.
* **Failures are retried, not dropped.** If any deploy fails the whole project is
  retried with exponential backoff (2 s, 4 s, 8 s … capped at 5 min). The
  backoff resets on the first fully successful replay.
* **Provisioning restarts are cheap.** Replay state is in memory; after a
  provisioning restart the loop re-learns each runtime's `bootId` on the first
  poll and only replays runtimes that are actually empty.

Multiple provisioning replicas each run the loop. Duplicate replays are
possible but harmless for the reason above.

## Configuration

| Variable | Default | Meaning |
|----------|---------|---------|
| `EXCALIBASE_FN_REPLAY_ENABLED` | `true` | Set to `false` to turn the loop off. |
| `EXCALIBASE_FN_REPLAY_POLL_MS` | `15000` | Poll interval per project runtime. |

Provisioning logs `Function replay on runtime cold start enabled (poll every 15s)`
at boot and `function replay: project=… bootId=… functions=N` for each replay.

## What is not covered

* **In-memory logs.** The runtime's per-function log ring buffer is not
  persisted; a restart still loses it.
* **Detection latency.** Between the pod becoming ready and the next poll
  (up to one interval) invokes against the restarted runtime return
  "function not found". Lower `EXCALIBASE_FN_REPLAY_POLL_MS` if that window
  matters more than the extra `/health` traffic.
