/**
 * Phase 8 — `cronJobs()` registry tests.
 *
 * The library ships an in-process registry shape. A user bundle that imports
 * `cronJobs` and calls `.cron(...)` / `.interval(...)` / `.daily(...)` /
 * `.hourly(...)` accumulates a table the Excalibase bundler reads at deploy
 * time via the `globalThis.__excalibase_crons` side-channel.
 *
 * The  API:
 *   const crons = cronJobs();
 *   crons.daily("send-digest", { hourUTC: 9, minuteUTC: 0 }, internal.jobs.sendDigest);
 *   export default crons;
 *
 * Library responsibilities:
 *   * register jobs by name (duplicates rejected with a clear error)
 *   * validate cron syntax (5-field min hour dom mon dow)
 *   * expose `getJobs()` returning a defensive copy used by the bundler
 *   * publish the table on `globalThis.__excalibase_crons` so the Go-side
 *     bundler scan can lift it off the bundled JS
 */

import {
  cronJobs,
  isCrons,
  makeFunctionRef,
  type Crons,
  type FunctionRef,
} from "../src";

const TEST_CRONS_KEY = "__excalibase_crons" as const;

function clearGlobalChannel(): void {
  // The lib writes the table into globalThis on every call; tests reset
  // the slot so registrations from one test don't leak into another.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  delete (globalThis as any)[TEST_CRONS_KEY];
}

describe("cronJobs() registry shape", () => {
  beforeEach(() => clearGlobalChannel());
  afterAll(() => clearGlobalChannel());

  it("cronJobs() returns a Crons object that isCrons() recognises", () => {
    const c = cronJobs();
    expect(isCrons(c)).toBe(true);
    expect(typeof c.cron).toBe("function");
    expect(typeof c.interval).toBe("function");
    expect(typeof c.daily).toBe("function");
    expect(typeof c.hourly).toBe("function");
    expect(typeof c.getJobs).toBe("function");
  });

  it("isCrons returns false for non-crons values", () => {
    expect(isCrons(null)).toBe(false);
    expect(isCrons({})).toBe(false);
    expect(isCrons({ cron: () => {}, interval: () => {} })).toBe(false);
  });

  it(".cron(name, expr, ref, args) registers a job with cron syntax", () => {
    const c: Crons = cronJobs();
    const ref = makeFunctionRef<{ x: number }, void>("jobs", "tick");
    c.cron("every-5-min", "*/5 * * * *", ref, { x: 1 });
    const jobs = c.getJobs();
    expect(jobs).toHaveLength(1);
    expect(jobs[0]).toEqual({
      name: "every-5-min",
      schedule: { kind: "cron", expression: "*/5 * * * *" },
      fnRef: ref,
      args: { x: 1 },
    });
  });

  it(".interval(name, schedule, ref, args) registers a recurring interval", () => {
    const c: Crons = cronJobs();
    const ref = makeFunctionRef<{}, void>("jobs", "heartbeat");
    c.interval("heartbeat", { minutes: 10 }, ref, {});
    const jobs = c.getJobs();
    expect(jobs).toHaveLength(1);
    expect(jobs[0]).toEqual({
      name: "heartbeat",
      schedule: { kind: "interval", hours: 0, minutes: 10, seconds: 0 },
      fnRef: ref,
      args: {},
    });
  });

  it(".daily(name, { hourUTC, minuteUTC }, ref, args) registers a daily job", () => {
    const c: Crons = cronJobs();
    const ref = makeFunctionRef<{}, void>("jobs", "sendDigest");
    c.daily("send-digest", { hourUTC: 9, minuteUTC: 30 }, ref, {});
    const jobs = c.getJobs();
    expect(jobs).toHaveLength(1);
    expect(jobs[0]).toEqual({
      name: "send-digest",
      schedule: { kind: "daily", hourUTC: 9, minuteUTC: 30 },
      fnRef: ref,
      args: {},
    });
  });

  it(".hourly(name, { minuteUTC }, ref, args) registers an hourly job", () => {
    const c: Crons = cronJobs();
    const ref = makeFunctionRef<{}, void>("jobs", "purgeStale");
    c.hourly("purge-stale", { minuteUTC: 17 }, ref, {});
    const jobs = c.getJobs();
    expect(jobs).toHaveLength(1);
    expect(jobs[0]).toEqual({
      name: "purge-stale",
      schedule: { kind: "hourly", minuteUTC: 17 },
      fnRef: ref,
      args: {},
    });
  });

  it("publishes the table on globalThis.__excalibase_crons for the bundler", () => {
    const c: Crons = cronJobs();
    c.daily("a", { hourUTC: 0, minuteUTC: 0 }, makeFunctionRef("m", "x"), {});
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const channel = (globalThis as any)[TEST_CRONS_KEY] as unknown;
    expect(Array.isArray(channel)).toBe(true);
    expect((channel as unknown[]).length).toBe(1);
  });

  it("getJobs() returns a defensive copy (mutating the result doesn't affect the registry)", () => {
    const c: Crons = cronJobs();
    c.daily("a", { hourUTC: 0, minuteUTC: 0 }, makeFunctionRef("m", "x"), {});
    const jobsA = c.getJobs();
    jobsA.length = 0;
    expect(c.getJobs()).toHaveLength(1);
  });
});

describe("cronJobs() validation", () => {
  beforeEach(() => clearGlobalChannel());
  afterAll(() => clearGlobalChannel());

  it("rejects duplicate names", () => {
    const c: Crons = cronJobs();
    const ref: FunctionRef = makeFunctionRef("m", "x");
    c.daily("dup", { hourUTC: 1, minuteUTC: 0 }, ref, {});
    expect(() => c.daily("dup", { hourUTC: 2, minuteUTC: 0 }, ref, {})).toThrow(/duplicate/i);
    expect(() => c.cron("dup", "* * * * *", ref, {})).toThrow(/duplicate/i);
  });

  it("rejects empty / non-string names", () => {
    const c: Crons = cronJobs();
    const ref: FunctionRef = makeFunctionRef("m", "x");
    expect(() => c.daily("", { hourUTC: 1, minuteUTC: 0 }, ref, {})).toThrow(/name/i);
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(() => c.daily(123 as any, { hourUTC: 1, minuteUTC: 0 }, ref, {})).toThrow(/name/i);
  });

  it("rejects non-FunctionRef values for the fnRef arg", () => {
    const c: Crons = cronJobs();
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(() => c.daily("a", { hourUTC: 1, minuteUTC: 0 }, null as any, {})).toThrow(/FunctionRef/);
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(() => c.daily("a", { hourUTC: 1, minuteUTC: 0 }, { foo: 1 } as any, {})).toThrow(/FunctionRef/);
  });

  it(".daily rejects out-of-range hour/minute", () => {
    const c: Crons = cronJobs();
    const ref: FunctionRef = makeFunctionRef("m", "x");
    expect(() => c.daily("a", { hourUTC: -1, minuteUTC: 0 }, ref, {})).toThrow(/hourUTC/);
    expect(() => c.daily("b", { hourUTC: 24, minuteUTC: 0 }, ref, {})).toThrow(/hourUTC/);
    expect(() => c.daily("c", { hourUTC: 0, minuteUTC: 60 }, ref, {})).toThrow(/minuteUTC/);
    expect(() => c.daily("d", { hourUTC: 0, minuteUTC: -1 }, ref, {})).toThrow(/minuteUTC/);
  });

  it(".hourly rejects out-of-range minute", () => {
    const c: Crons = cronJobs();
    const ref: FunctionRef = makeFunctionRef("m", "x");
    expect(() => c.hourly("a", { minuteUTC: -1 }, ref, {})).toThrow(/minuteUTC/);
    expect(() => c.hourly("b", { minuteUTC: 60 }, ref, {})).toThrow(/minuteUTC/);
  });

  it(".interval rejects schedules with no positive component", () => {
    const c: Crons = cronJobs();
    const ref: FunctionRef = makeFunctionRef("m", "x");
    expect(() => c.interval("a", {}, ref, {})).toThrow(/at least one/i);
    expect(() => c.interval("b", { hours: 0, minutes: 0, seconds: 0 }, ref, {})).toThrow(/at least one/i);
    expect(() => c.interval("c", { seconds: -1 }, ref, {})).toThrow(/non-negative/i);
  });

  it(".cron rejects malformed cron expressions", () => {
    const c: Crons = cronJobs();
    const ref: FunctionRef = makeFunctionRef("m", "x");
    // Cron syntax requires exactly 5 fields.
    expect(() => c.cron("a", "* * *", ref, {})).toThrow(/cron/i);
    expect(() => c.cron("b", "* * * * * *", ref, {})).toThrow(/cron/i);
    // Field out of range.
    expect(() => c.cron("c", "60 * * * *", ref, {})).toThrow(/cron|minute/i);
    expect(() => c.cron("d", "* 24 * * *", ref, {})).toThrow(/cron|hour/i);
  });

  it(".cron accepts canonical  examples", () => {
    const c: Crons = cronJobs();
    const ref: FunctionRef = makeFunctionRef("m", "x");
    c.cron("a", "*/5 * * * *", ref, {});
    c.cron("b", "0 0 * * 0", ref, {});
    c.cron("c", "0 9 * * 1-5", ref, {});
    c.cron("d", "30 14 1 * *", ref, {});
    c.cron("e", "0,15,30,45 * * * *", ref, {});
    c.cron("f", "0 0-23/2 * * *", ref, {});
    expect(c.getJobs()).toHaveLength(6);
  });

  it(".cron rejects assorted bad expressions", () => {
    const c: Crons = cronJobs();
    const ref: FunctionRef = makeFunctionRef("m", "x");
    expect(() => c.cron("a", "", ref, {})).toThrow(/cron/i);
    expect(() => c.cron("b", "*/abc * * * *", ref, {})).toThrow(/cron|step/i);
    expect(() => c.cron("c", "*/0 * * * *", ref, {})).toThrow(/cron|step/i);
    expect(() => c.cron("d", "1-x * * * *", ref, {})).toThrow(/cron|range/i);
    expect(() => c.cron("e", "5-3 * * * *", ref, {})).toThrow(/cron|range/i);
    expect(() => c.cron("f", "1,,3 * * * *", ref, {})).toThrow(/empty|cron/i);
    expect(() => c.cron("g", "abc * * * *", ref, {})).toThrow(/cron|integer/i);
    expect(() => c.cron("h", "* 0 32 * *", ref, {})).toThrow(/cron|dayOfMonth/i);
    expect(() => c.cron("i", "* * * 13 *", ref, {})).toThrow(/cron|month/i);
    expect(() => c.cron("j", "* * * * 8", ref, {})).toThrow(/cron|dayOfWeek/i);
  });

  it(".interval and .daily reject undefined/null schedule arg", () => {
    const c: Crons = cronJobs();
    const ref: FunctionRef = makeFunctionRef("m", "x");
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(() => c.daily("a", null as any, ref, {})).toThrow(/schedule/i);
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(() => c.hourly("b", null as any, ref, {})).toThrow(/schedule/i);
    expect(() => c.interval("c", { hours: 1.5 }, ref, {})).toThrow(/integer/i);
    expect(() => c.interval("d", { minutes: 1.5 }, ref, {})).toThrow(/integer/i);
  });

  it(".cron / .daily / .hourly / .interval reject non-object args", () => {
    const c: Crons = cronJobs();
    const ref: FunctionRef = makeFunctionRef("m", "x");
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(() => c.daily("a", { hourUTC: 1, minuteUTC: 0 }, ref, [] as any)).toThrow(/args/i);
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(() => c.cron("b", "* * * * *", ref, null as any)).toThrow(/args/i);
  });

  it("name longer than 128 characters is rejected", () => {
    const c: Crons = cronJobs();
    const ref: FunctionRef = makeFunctionRef("m", "x");
    const longName = "a".repeat(129);
    expect(() => c.daily(longName, { hourUTC: 0, minuteUTC: 0 }, ref, {})).toThrow(/name/i);
  });
});
