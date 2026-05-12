# Functions: transactions, isolation, and retries

Excalibase functions ride a single Postgres transaction per top-level
mutation invocation. The runtime opens the transaction before the
handler runs, routes every `ctx.db.*` and `ctx.scheduler.*` call through
that one connection, and commits on success or rolls back on throw. This
mirrors Convex's atomicity model: a mutation either applies fully or
not at all.

## Isolation level (Phase 9a)

Top-level mutations BEGIN with `ISOLATION LEVEL SERIALIZABLE` by
default. SERIALIZABLE catches read-write conflicts at COMMIT time (via
Postgres' Serializable Snapshot Isolation, SSI), which gives Excalibase
mutations the same conflict-detection surface that Convex's optimistic
concurrency control provides natively.

Operators can override the isolation level via the
`EXCALIBASE_MUTATION_ISOLATION` environment variable on the Deno
runtime pod:

```
EXCALIBASE_MUTATION_ISOLATION=SERIALIZABLE   # default
EXCALIBASE_MUTATION_ISOLATION="REPEATABLE READ"
EXCALIBASE_MUTATION_ISOLATION="READ COMMITTED"
```

The value is validated at boot — an unknown string causes the runtime
to exit with a fatal error so a typo in a Helm value can never silently
downgrade isolation in production. Anything outside the three values
above is rejected.

| Isolation level   | Conflict surface     | Cost         | When to pick                                              |
|-------------------|----------------------|--------------|-----------------------------------------------------------|
| SERIALIZABLE      | SSI at COMMIT        | Predicate-lock bookkeeping | Default. Matches Convex semantics. |
| REPEATABLE READ   | RR at first write    | Mid-range    | If your workload is read-heavy with rare conflicts and SSI overhead is measured to hurt p99. |
| READ COMMITTED    | Statement-local only | Lowest       | If you've measured under-contention p99 spikes from SSI and accept weaker guarantees. |

## Retry-on-conflict (Phase 9a)

When a mutation hits Postgres SQLSTATE `40001` (serialization failure)
or `40P01` (deadlock detected), the runtime rolls the transaction back,
sleeps with exponential backoff + jitter, and re-invokes the handler
from scratch. The handler runs fresh — no partially-committed state
ever leaks into the retry.

Two SQLSTATEs trigger retry:

- `40001` — `serialization_failure`. Raised by the SSI manager when
  the transactions cannot be ordered without a real conflict. Often
  surfaces at COMMIT time under SERIALIZABLE.
- `40P01` — `deadlock_detected`. Raised when the deadlock detector
  kills one side of a lock-cycle. Always surfaces mid-statement.

Everything else (`23505` unique violation, `42703` undefined column,
etc.) bubbles up unchanged — the runtime never silently retries a
logic error.

Tuning knobs:

```
EXCALIBASE_MUTATION_RETRY_MAX=5         # default; clamped to [1, 50]
EXCALIBASE_MUTATION_RETRY_BACKOFF_MS=50  # default base
```

The backoff schedule is:

```
sleepMs(attempt) = min(5000, floor(base * 2^(attempt-1) * (0.5 + random())))
```

With defaults (base = 50ms), the first five sleeps are approximately
`[25-50, 50-100, 100-200, 200-400, 400-800]` ms.

## What surfaces to callers

When the retry loop exhausts its max attempts on a retryable conflict,
the runtime returns an HTTP 409 with a structured body:

```json
{
  "error": "MUTATION_CONFLICT",
  "attempts": 5,
  "code": "40001",
  "message": "could not serialize access due to read/write dependencies among transactions"
}
```

In TypeScript, callers using `@excalibase/server` can catch this via
the `ConflictError` class (introduced in `@excalibase/server@0.7.1`):

```ts
import { ConflictError } from "@excalibase/server";

try {
  await ctx.runMutation(internal.x.transfer, args);
} catch (err) {
  if (err instanceof ConflictError) {
    // give up, surface to user, or fall back to a different path —
    // this is a real contention exhaustion, not a logic bug.
  }
  throw err;
}
```

## Composition semantics

- **Top-level mutations** retry on `40001`/`40P01`.
- **Actions** never retry. They have no transaction boundary, and
  re-running them is generally unsafe because they may have already
  fired external side effects (HTTP calls, etc).
- **Nested `ctx.runMutation`** rides the parent's transaction — it
  does NOT open a new one and does NOT independently retry. If the
  parent retries, the nested call runs again as part of the parent's
  re-execution.

## Observability

Prometheus counter on the runtime pod:

```
excalibase_mutation_retries_total
```

Each retry attempt increments by 1; an exhausted retry adds
`MUTATION_RETRY_MAX - 1` to the counter and yields a 409 to the caller.

A sustained increase on this counter usually means one of:
1. Hot rows / hot predicates causing real contention — consider
   partitioning, narrower predicates, or adding an index that lets
   SSI track a finer predicate.
2. Long-running mutations holding the connection — under contention
   the pool can starve. The retry loop holds the reserved connection
   for the duration of all attempts.
3. An anti-pattern: many mutations writing to the same global counter
   row. Convex parity: this same pattern would cause OCC retries on
   Convex too.

## Pool-exhaustion warning

Under sustained high contention with long mutation handlers, the retry
loop holds one connection per in-flight mutation for the full retry
duration. The pool has a fixed max — see `runtime/pool.ts` — and
exhausting it makes new mutations queue. Watch `pg_stat_activity` for
connections in `active`/`idle in transaction` state and the runtime's
`excalibase_mutation_retries_total` counter together. If both are
elevated, scale the connection pool or shrink the conflict surface
before raising `EXCALIBASE_MUTATION_RETRY_MAX`.
