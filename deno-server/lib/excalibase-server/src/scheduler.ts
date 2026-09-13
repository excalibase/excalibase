/**
 * Scheduler types.
 *
 * The library declares the contract for `ctx.scheduler`; the runtime is
 * responsible for the actual queue, persistence, and dispatch.
 *
 *   * `runAfter(delayMs, fnRef, args): Promise<ScheduledId>`
 *   * `runAt(timestamp, fnRef, args): Promise<ScheduledId>`
 *   * `cancel(id): Promise<void>`
 *
 * Transactional semantics today: the enqueue write commits immediately on
 * the project DB. Future iterations may re-introduce a shared transaction
 * with a mutation's database writes.
 *
 * Queries deliberately do NOT carry a scheduler (read-only contract).
 */

import type { FunctionRef } from "./types";

/**
 * Opaque identifier returned by `Scheduler.runAfter` / `Scheduler.runAt`.
 * The runtime emits a 30-character base32 token; the static type stays a
 * plain string so logging, persistence, and JSON serialisation work without
 * translation. Callers should treat the value as opaque and never parse it.
 */
export type ScheduledId = string;

/**
 * The `ctx.scheduler` surface attached to mutation and action contexts.
 * Library users get a typed object; the runtime supplies the real
 * implementation by routing each method through a main-thread RPC channel.
 */
export interface Scheduler {
  /**
   * Enqueue a function call for `Date.now() + delayMs`. Negative or
   * zero delays are clamped to "now" by the runtime, which then drains
   * the entry on its next scheduler tick.
   */
  runAfter<TArgs, TResult>(
    delayMs: number,
    fnRef: FunctionRef<TArgs, TResult>,
    args: TArgs,
  ): Promise<ScheduledId>;

  /**
   * Enqueue a function call for an absolute millisecond timestamp.
   * Timestamps in the past behave the same as `runAfter(0, ...)`.
   */
  runAt<TArgs, TResult>(
    timestamp: number,
    fnRef: FunctionRef<TArgs, TResult>,
    args: TArgs,
  ): Promise<ScheduledId>;

  /**
   * Cancel a previously scheduled function. If the task has not yet
   * started, it is marked cancelled and never runs. If the task has
   * already started,  parity says it continues to completion but
   * any functions IT scheduled will not run — the runtime enforces this.
   * Idempotent: cancelling an unknown / already-completed id is a no-op.
   */
  cancel(id: ScheduledId): Promise<void>;
}
