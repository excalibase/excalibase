# Project activity and idle auto-pause

Free-tier projects that nobody uses still hold a database, a runtime and a
slice of the node. Provisioning tracks when each project was last touched
(EXC-279) and, on tiers that opt in, warns the owner and then pauses the
project once it has sat idle for long enough (EXC-280). A paused project keeps
its data (a backup is taken first) and resumes from the dashboard or the API.

## What counts as activity

The data plane (GraphQL, REST, realtime) does not proxy through provisioning,
so the platform records the signals it does see. Every **successful** (status
< 400) request that carries a project id bumps `project_activity`:

| Source              | Route family                                   | Who triggers it                                                    |
|---------------------|------------------------------------------------|--------------------------------------------------------------------|
| `policy_fetch`      | `/api/provision/{id}/rls-policies`, `column-policies` | excalibase-graphql refreshing its policy cache (30 s TTL) — only while the project serves traffic, so this is the data-plane heartbeat |
| `function_invoke`   | `/functions/v1/{id}/…`, `/internal/invoke/{id}/…` | end users calling edge functions, functions calling each other  |
| `info`              | `/api/projects/{id}/info`                      | the auth service minting an end-user JWT                           |
| `functions`         | `/api/projects/{id}/functions/…`               | authoring, deploying, invoking from Studio                         |
| `schema`            | `/api/schema/{id}/…`, `/api/projects/{id}/schema` | Studio table browser, DDL, row edits                            |
| `migration`, `backup`, `realtime`, `storage` | the matching subtrees          | Studio / CLI                                                       |
| `api`               | any other `/api/provision/{id}/…` call         | Studio, CLI, SDK                                                   |

Rejected calls (401/403/404) never count, so a stranger probing project ids
cannot keep a project alive. Writes are throttled in memory to **one per
project per 5 minutes**; the throttle map is capped at 10 000 projects, beyond
which writes still happen but are not throttled.

The marker is exposed as `lastSeenAt` on `GET /api/provision/{id}` and the
project list; `idleWarnedAt` appears while a warning is outstanding.

The table lives next to the instances:

```sql
project_activity(project_id PK → database_instances ON DELETE CASCADE,
                 last_seen_at, last_seen_source, idle_warned_at)
```

## The sweep

Once an hour (cloud replicas elect a leader through a Postgres advisory lock)
the platform walks every `ACTIVE` project whose tier has
`autoPauseAfterDays = N > 0`:

* effective last-seen = the latest of `project_activity.last_seen_at`, the
  project's `created_at` (projects never seen at all) and its last resume;
* idle for **N days or more** → `PauseService.Pause(id, "idle")` — pre-pause
  backup, workload stopped, status `PAUSED`, `pauseReason = idle`;
* idle for **N-1 days or more** → one warning per idle stretch: an audit-log
  entry `project.idle_warning`, `idle_warned_at` on the activity row, and an
  email to the project owner when an email provider is configured
  (`EMAIL_PROVIDER`; with `noop` the audit log and `idleWarnedAt` are the only
  channel). Fresh activity clears the warning so the next idle stretch warns
  again.

A project resumed less than 24 hours ago is never paused, whatever its history.
Each pause is audited as `project.idle_pause`. A project whose deployment mode
has no pauser wired is skipped by `PauseService`.

## Configuration

| Setting | Where | Default |
|---------|-------|---------|
| `autoPauseAfterDays` per tier | `tier_configs` — `PUT /api/admin/tiers/{tier}`, Studio → Platform admin → Tier configuration | FREE = 7, STANDARD / ENTERPRISE = 0 (never) |
| `EXCALIBASE_AUTOPAUSE_ENABLED` | environment | `true` when `DEPLOYMENT_MODE=cloud`, `false` self-hosted |

Set a tier to `0` to exempt it entirely. The warning lead time is always one
day, so a threshold of `1` warns on the first sweep after creation.
