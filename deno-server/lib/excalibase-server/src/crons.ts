/**
 * Cron registry.
 *
 * A user bundle imports `cronJobs` from this library, calls
 * `.cron(...)` / `.interval(...)` / `.daily(...)` / `.hourly(...)` to
 * register jobs, and default-exports the resulting `Crons` object from
 * `crons.ts`. The Excalibase bundler picks up the job table through the
 * `globalThis.__excalibase_crons` side-channel.
 *
 * The library does NOT schedule jobs — it only validates input and
 * publishes the registry for the Go-side cron runner.
 */

import { isFunctionRef } from "./refs";
import type { FunctionRef } from "./types";

/**
 * The on-the-wire cron-channel slot. Lives on `globalThis` so the
 * Excalibase Go bundler can pick the array literal out of the
 * post-esbuild output without re-evaluating the module graph.
 */
const GLOBAL_CRONS_KEY = "__excalibase_crons" as const;

const CRONS_BRAND = Symbol.for("excalibase.cronJobs");

/**
 * Schedule descriptor — one of four shapes. Stored verbatim on the
 * `Crons` record so the runtime knows which kind of trigger to set up.
 */
export type CronSchedule =
  | { readonly kind: "cron"; readonly expression: string }
  | {
      readonly kind: "interval";
      readonly hours: number;
      readonly minutes: number;
      readonly seconds: number;
    }
  | { readonly kind: "daily"; readonly hourUTC: number; readonly minuteUTC: number }
  | { readonly kind: "hourly"; readonly minuteUTC: number };

/**
 * One job entry — name + schedule + target FunctionRef + args.
 */
export interface CronJob {
  readonly name: string;
  readonly schedule: CronSchedule;
  readonly fnRef: FunctionRef;
  readonly args: Readonly<Record<string, unknown>>;
}

/**
 * Crons registry returned by `cronJobs()`. Methods are chainable in spirit
 * but return `void` so user code reads as a sequence of
 * `c.daily(...); c.hourly(...);` statements, not a fluent chain.
 */
export interface Crons {
  cron(name: string, expression: string, fnRef: FunctionRef, args: Readonly<Record<string, unknown>>): void;
  interval(
    name: string,
    schedule: { readonly hours?: number; readonly minutes?: number; readonly seconds?: number },
    fnRef: FunctionRef,
    args: Readonly<Record<string, unknown>>,
  ): void;
  daily(
    name: string,
    schedule: { readonly hourUTC: number; readonly minuteUTC: number },
    fnRef: FunctionRef,
    args: Readonly<Record<string, unknown>>,
  ): void;
  hourly(
    name: string,
    schedule: { readonly minuteUTC: number },
    fnRef: FunctionRef,
    args: Readonly<Record<string, unknown>>,
  ): void;
  /** Defensive copy of the registered job table. Used by tests and bundler. */
  getJobs(): CronJob[];
}

/**
 * Construct a new cron registry. The returned object captures `jobs` in
 * its closure and mirrors the table into `globalThis.__excalibase_crons`
 * on every registration so the Go-side bundler scan finds an up-to-date
 * snapshot regardless of which call ran last.
 *
 * Multiple `cronJobs()` calls in the same bundle are NOT merged — the
 * global side-channel reflects the LAST registry that mutated it. Users
 * should declare one registry per bundle (`crons.ts` convention).
 */
export function cronJobs(): Crons {
  const jobs: CronJob[] = [];
  const names = new Set<string>();

  const publish = (): void => {
    // Defensive shallow-clone the array on each publish so a bundler that
    // already captured the reference doesn't see in-place mutations. The
    // entries themselves are frozen at registration time.
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (globalThis as any)[GLOBAL_CRONS_KEY] = jobs.map((j) => j);
  };

  const register = (job: CronJob): void => {
    if (names.has(job.name)) {
      throw new Error(`cronJobs: duplicate job name "${job.name}"`);
    }
    names.add(job.name);
    jobs.push(Object.freeze(job));
    publish();
  };

  const registry = {
    [CRONS_BRAND]: true as const,
    cron(name, expression, fnRef, args) {
      validateName(name);
      validateFnRef(fnRef);
      validateArgs(args);
      validateCronExpression(expression);
      register({
        name,
        schedule: { kind: "cron", expression },
        fnRef,
        args,
      });
    },
    interval(name, schedule, fnRef, args) {
      validateName(name);
      validateFnRef(fnRef);
      validateArgs(args);
      const hours = schedule?.hours ?? 0;
      const minutes = schedule?.minutes ?? 0;
      const seconds = schedule?.seconds ?? 0;
      if (!Number.isInteger(hours) || hours < 0) {
        throw new Error(`cronJobs.interval: hours must be a non-negative integer (got ${hours})`);
      }
      if (!Number.isInteger(minutes) || minutes < 0) {
        throw new Error(`cronJobs.interval: minutes must be a non-negative integer (got ${minutes})`);
      }
      if (!Number.isInteger(seconds) || seconds < 0) {
        throw new Error(`cronJobs.interval: seconds must be a non-negative integer (got ${seconds})`);
      }
      if (hours + minutes + seconds === 0) {
        throw new Error("cronJobs.interval: at least one of hours/minutes/seconds must be > 0");
      }
      register({
        name,
        schedule: { kind: "interval", hours, minutes, seconds },
        fnRef,
        args,
      });
    },
    daily(name, schedule, fnRef, args) {
      validateName(name);
      validateFnRef(fnRef);
      validateArgs(args);
      if (!schedule || typeof schedule !== "object") {
        throw new Error("cronJobs.daily: schedule must be an object");
      }
      const { hourUTC, minuteUTC } = schedule;
      if (!Number.isInteger(hourUTC) || hourUTC < 0 || hourUTC > 23) {
        throw new Error(`cronJobs.daily: hourUTC must be an integer in [0, 23] (got ${hourUTC})`);
      }
      if (!Number.isInteger(minuteUTC) || minuteUTC < 0 || minuteUTC > 59) {
        throw new Error(`cronJobs.daily: minuteUTC must be an integer in [0, 59] (got ${minuteUTC})`);
      }
      register({
        name,
        schedule: { kind: "daily", hourUTC, minuteUTC },
        fnRef,
        args,
      });
    },
    hourly(name, schedule, fnRef, args) {
      validateName(name);
      validateFnRef(fnRef);
      validateArgs(args);
      if (!schedule || typeof schedule !== "object") {
        throw new Error("cronJobs.hourly: schedule must be an object");
      }
      const { minuteUTC } = schedule;
      if (!Number.isInteger(minuteUTC) || minuteUTC < 0 || minuteUTC > 59) {
        throw new Error(`cronJobs.hourly: minuteUTC must be an integer in [0, 59] (got ${minuteUTC})`);
      }
      register({
        name,
        schedule: { kind: "hourly", minuteUTC },
        fnRef,
        args,
      });
    },
    getJobs(): CronJob[] {
      return jobs.slice();
    },
  } as Crons & { [CRONS_BRAND]: true };

  // Publish the empty table once at construction so the bundler-side scan
  // can detect "cronJobs() was called but nothing registered" — a legal
  // (if useless) deploy state.
  publish();

  return registry;
}

/**
 * Runtime guard for `cronJobs()` results. The brand symbol survives the
 * esbuild IIFE round-trip, so a hand-rolled `{ cron, interval, ... }`
 * literal returns `false`.
 */
export function isCrons(value: unknown): value is Crons {
  if (value === null || typeof value !== "object") return false;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return (value as any)[CRONS_BRAND] === true;
}

// ---------------------------------------------------------------------------
// Validation helpers — kept private; the public API is the four `c.<kind>(...)`
// methods + `getJobs()` + `isCrons()`.
// ---------------------------------------------------------------------------

function validateName(name: unknown): asserts name is string {
  if (typeof name !== "string" || name.length === 0) {
    throw new Error("cronJobs: name must be a non-empty string");
  }
  if (name.length > 128) {
    throw new Error("cronJobs: name must be at most 128 characters");
  }
}

function validateFnRef(fnRef: unknown): asserts fnRef is FunctionRef {
  if (!isFunctionRef(fnRef)) {
    throw new Error("cronJobs: fnRef must be a FunctionRef { moduleName, exportName }");
  }
}

function validateArgs(args: unknown): asserts args is Readonly<Record<string, unknown>> {
  if (args === null || typeof args !== "object" || Array.isArray(args)) {
    throw new Error("cronJobs: args must be a plain object");
  }
}

/**
 * 5-field cron parser — `minute hour day-of-month month day-of-week`.
 *
 * Standard Quartz-style extensions (`?`, `L`, `W`, year field) are NOT
 * accepted. The runner uses `robfig/cron`'s standard parser on the Go
 * side; this validator just rejects shape mistakes early so a bad
 * expression fails at deploy time rather than at the first cron tick.
 */
const CRON_FIELD_BOUNDS: ReadonlyArray<readonly [number, number, string]> = [
  [0, 59, "minute"],
  [0, 23, "hour"],
  [1, 31, "dayOfMonth"],
  [1, 12, "month"],
  [0, 7, "dayOfWeek"], // 0 and 7 both denote Sunday — match cron(5).
];

export function validateCronExpression(expression: string): void {
  if (typeof expression !== "string" || expression.length === 0) {
    throw new Error("cronJobs.cron: expression must be a non-empty string");
  }
  const fields = expression.trim().split(/\s+/);
  if (fields.length !== 5) {
    throw new Error(
      `cronJobs.cron: expression must have exactly 5 fields (minute hour dom mon dow); got ${fields.length}`,
    );
  }
  for (let i = 0; i < 5; i++) {
    const [min, max, name] = CRON_FIELD_BOUNDS[i];
    validateCronField(fields[i], min, max, name);
  }
}

/**
 * Validate a single cron field. Accepts:
 *   * `*`            — any value
 *   * `N`            — single integer in [min, max]
 *   * `N-M`          — range
 *   * `*\/N` or `N/M`— step
 *   * `a,b,c`        — comma-separated list of any of the above
 *
 * Named day/month literals (`SUN`, `JAN`, etc.) are not yet recognised — the
 * runner doesn't need them, and adding parsing for them only makes the
 * surface bigger. The Go side rejects them too.
 */
function validateCronField(field: string, min: number, max: number, name: string): void {
  if (field === "*") return;
  // Comma list — recurse per element.
  if (field.includes(",")) {
    for (const part of field.split(",")) {
      if (part.length === 0) {
        throw new Error(`cronJobs.cron: empty entry in ${name} list`);
      }
      validateCronField(part, min, max, name);
    }
    return;
  }
  // Step (e.g. `*/5`, `0/15`, `1-30/3`).
  if (field.includes("/")) {
    const [head, stepStr] = field.split("/");
    if (head === undefined || stepStr === undefined || stepStr.length === 0) {
      throw new Error(`cronJobs.cron: malformed step in ${name} (${field})`);
    }
    const step = Number(stepStr);
    if (!Number.isInteger(step) || step <= 0) {
      throw new Error(`cronJobs.cron: step in ${name} must be a positive integer (got ${stepStr})`);
    }
    if (head !== "*") {
      validateCronField(head, min, max, name);
    }
    return;
  }
  // Range (e.g. `1-5`).
  if (field.includes("-")) {
    const [a, b] = field.split("-");
    if (a === undefined || b === undefined) {
      throw new Error(`cronJobs.cron: malformed range in ${name} (${field})`);
    }
    const lo = Number(a);
    const hi = Number(b);
    if (!Number.isInteger(lo) || !Number.isInteger(hi)) {
      throw new Error(`cronJobs.cron: ${name} range bounds must be integers (got ${field})`);
    }
    if (lo < min || hi > max || lo > hi) {
      throw new Error(`cronJobs.cron: ${name} range out of [${min}, ${max}] (got ${field})`);
    }
    return;
  }
  // Single integer.
  const n = Number(field);
  if (!Number.isInteger(n)) {
    throw new Error(`cronJobs.cron: ${name} field must be an integer (got ${field})`);
  }
  if (n < min || n > max) {
    throw new Error(`cronJobs.cron: ${name} field out of [${min}, ${max}] (got ${n})`);
  }
}
