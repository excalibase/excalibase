# Function scheduler

Excalibase Functions can defer work (`ctx.scheduler.runAfter` / `runAt`) and
declare cron jobs (`cronJobs()`). Both are stored in the **tenant's own
database**, in the reserved `excalibase` schema:

| Table | Written by | Read by |
| --- | --- | --- |
| `excalibase.excalibase_scheduled_functions` | the runtime's scheduler RPCs, and the cron sweep | the task sweep |
| `excalibase.excalibase_cron_jobs` | the deploy-time cron sync | the cron sweep |

Because the rows live per tenant, the control plane sweeps **every servable
project**, not one database. Projects are listed from the platform's project
rows and opened with the excalibase_app credentials filed in vault — the same
way the schema browser, migrations and the restore probe reach a tenant. A
project whose database carries no scheduler tables is left alone; one that is
DELETING or RESTORING is never listed.

## The two halves

* **Tasks** — every replica may run this half. Due rows are claimed with
  `FOR UPDATE SKIP LOCKED`, so two replicas never take the same row.
* **Cron** — leader only, and the registry walk is scoped to the swept
  project in SQL. In cloud mode the claim is a Postgres advisory lock on the
  **platform** database, so it is a standing connection there for as long as
  this replica leads — counted in the pool arithmetic in `OPERATOR.md`. An enqueue is decided by comparing the next due
  time with `last_enqueued_at`, which is not a claim: two replicas reading
  before either writes would both insert. The leader is elected with a
  Postgres advisory lock in cloud mode and is a no-op claim in a
  single-process deployment.

## The tenant owns these tables

The project holds its database's credentials, so it can write any row into
the reserved `excalibase` schema. Nothing read from there is authority:

| Column | Read as | Why |
| --- | --- | --- |
| `project_id` | checked, never trusted | the sweep's project is the authority; a row naming another project is closed as `failed` |
| `module_name` | data, validated | must be a plain identifier AND a function the platform deployed for this project |
| `export_name` | data, validated | must be a plain export identifier |
| `args` | data, bounded | must be JSON within the invoke body limit — bounded in SQL, so an oversized payload is failed without being read |
| `attempts` | data, clamped | cannot widen the platform's retry budget |
| `scheduled_for`, `status` | data | select rows only; a lying value costs the tenant its own slot |
| cron `schedule` | data, clipped | cadences finer than the platform minimum are clipped up |
| cron `name`, `args`, `schedule` | data, bounded | name is an identifier within the project only; lengths and payload sizes are bounded in SQL, so an out-of-bounds row is never walked |

A row failing any check is closed with fixed platform text; tenant content
is never echoed back into the row or into a log line as a format.

## Limits

| Setting | Default | Bounds |
| --- | --- | --- |
| `EXCALIBASE_SCHEDULER_BATCH` | 32 | rows claimed per project per tick |
| `EXCALIBASE_SCHEDULER_PROJECT_CONCURRENCY` | 4 | invocations in flight per project |
| `EXCALIBASE_SCHEDULER_CONCURRENCY` | 32 | invocations in flight on this replica |
| `EXCALIBASE_SCHEDULER_MAX_ARGS_BYTES` | 1 MiB | one task's args (the public invoke body limit) |
| `EXCALIBASE_SCHEDULER_MAX_ATTEMPTS` | 5 | retries before a task is failed |
| `EXCALIBASE_CRON_MIN_INTERVAL_MS` | 60000 | finest cron cadence honoured |
| `EXCALIBASE_CRON_MAX_JOBS` | 100 | cron rows read per project per tick |
| `EXCALIBASE_SCHEDULER_PROJECT_TIMEOUT_MS` | 30000 | one project's whole sweep |
| `EXCALIBASE_PROJECT_DB_STATEMENT_TIMEOUT_MS` | 30000 | any statement on a tenant connection (server-side) |
| `EXCALIBASE_PROJECT_DB_LOCK_TIMEOUT_MS` | 5000 | waiting for a lock on a tenant connection (server-side) |
| `EXCALIBASE_SCHEDULER_CLAIM_LEASE_MS` | 300000 | how long a claimed task may stay `running` before it is taken back |

Worst case per tenant per minute, at the defaults: 12 task ticks × 32 rows =
**384 dispatch attempts**, never more than **4 at once**, each bounded by the
40s invocation timeout; plus **1 cron walk of at most 100 rows**, which can
enqueue at most 100 tasks that then pass through the same task limits. Per
replica, no more than **32 invocations are ever in flight** across all
tenants. Database load per tenant is one claim transaction per tick, one
cached presence check, and one cron query per minute on the leader.

### A tenant that will not answer

The scheduler tables live in the tenant's own database, so a project can
replace one with a view that calls `pg_sleep`, or simply hold a lock on it.
Three bounds keep that cost to the one tenant. Each project's sweep runs
under its own deadline, after which the sweep moves on to the next project;
every tenant connection carries a server-side `statement_timeout` and
`lock_timeout`, so abandoning the client side does not leave the statement
running there; and a sweep that hits its deadline counts as a failure, which
puts the project into the existing per-project backoff (5s doubling to 5
minutes) instead of costing a full timeout on every tick.

## Dispatch

A scheduled call is data-plane work: it takes no per-project lifecycle
lease, so a pause, resume or delete in flight is never blocked by it — but
it only ever runs for an ACTIVE project, so it cannot race one either.

A due task is dispatched through the same path a user invocation takes: the
project's runtime client (per-project service, per-project runtime
credential), `POST /invoke/{projectId}__{moduleName}` with `{args}` as the
body, bounded by a timeout.

Row outcomes:

| Outcome | Status |
| --- | --- |
| the function answered | `completed` |
| the runtime failed | `pending` again with exponential backoff, up to 5 attempts, then `failed` |
| the project may not be served | `skipped` — nothing was dispatched and no attempt is spent |
| the replica died holding the claim | `pending` again once the claim lease expires, one attempt spent, `failed` when the budget runs out |

### The claim lease

A claim commits `status='running'` and stamps `claimed_at`. If the replica
dies, or the runtime never answers, that row would otherwise stay `running`
forever — the claim only ever re-reads `pending`. Each sweep first takes back
any row whose `claimed_at` is older than
`EXCALIBASE_SCHEDULER_CLAIM_LEASE_MS` (default 5 minutes, comfortably past
the 40s invocation timeout), spending one attempt on it, so a task that kills
whatever picks it up ends `failed` instead of looping.

A project database whose scheduler tables predate `claimed_at` makes every
sweep statement fail on the unknown column. That is deliberate: the sweep
logs it and backs the project off rather than guessing. Redeploying any
function applies the column.

## Settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `EXCALIBASE_SCHEDULER_ENABLED` | `true` | run the sweep in this process |
| `EXCALIBASE_SCHEDULER_POLL_MS` | `5000` | task sweep cadence |
| `EXCALIBASE_CRON_POLL_MS` | `60000` | cron sweep cadence |

An unreadable cadence stops the process rather than being replaced with a
default the operator did not choose.

## Connection cost

Each project pool is capped at `EXCALIBASE_PROJECT_DB_MAX_CONNS` (default 2,
one idle) with a 5-minute idle timeout and a 30-minute lifetime, and at most
`EXCALIBASE_PROJECT_DB_MAX_POOLS` (default 64) pools are cached per replica,
the least recently used being closed first. Worst case per replica:
64 × 2 = **128 connections and file descriptors**. Per tenant database, the
control plane takes at most 2 connections per replica — 6 across three
replicas, against a default `max_connections` of 100. A project that stops
being servable has its pool closed immediately (the deletion observer), and
a project with no scheduler tables is re-checked only every 5 minutes, so
its pool goes idle and drops its connection. Raise `MAX_POOLS` above the
number of projects that actually use the scheduler to avoid churn.

## Startup wiring check

Every feature flag declares what it needs in `internal/wiring`. The check
runs once in `main` before the server listens, and an enabled feature whose
dependency is missing stops the boot with a message naming both. A flag with
no entry in the table fails the package's own test, so the next flag cannot
be added unwired.

Covered today: the function scheduler (invoker, function registry, project
list, project database resolver, cron leader), schema auto-migration
(project database resolver), idle auto-pause (pause service), cold-start
replay (function runtime), and the provider selections — the named email
provider with its credentials, object storage and the backup destination,
each refused when named but only half configured. Environment reads live in
`internal/config`; a test scans the tree and fails when a setting is read
anywhere else without an allow-list entry.
