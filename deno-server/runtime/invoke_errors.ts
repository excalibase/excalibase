// How a function's failure travels: a FunctionError refuses with its 4xx
// status; anything else is a crash whose message reaches a nested ctx.run*
// caller but never the caller of /invoke (the worker logged it).

export interface InvokeResponse {
  status: number;
  headers: Record<string, string>;
  body: string;
  /** The handler crashed; body holds its message, for nested callers only. */
  failed?: boolean;
}

export type RunXFailure = {
  ok: false;
  error: string;
  errorName?: string;
  issues?: unknown;
  status?: number;
};

const CRASH_BODY = JSON.stringify({ error: "internal error" });

export function withoutCrashDetail(response: InvokeResponse): InvokeResponse {
  const { failed, ...rest } = response;
  return failed ? { ...rest, body: CRASH_BODY } : rest;
}

// runXTargetError is what a nested call throws in its caller when the target
// did not succeed: its validation issues, its refusal status, or its message.
export function runXTargetError(status: number, parsed: { error?: string; issues?: unknown }): Error {
  const err = new Error(parsed.error || `runX target returned ${status}`);
  if (parsed.issues) {
    Object.assign(err, { issues: parsed.issues });
    err.name = "ValidationError";
  } else if (status >= 400 && status <= 499) {
    Object.assign(err, { status });
    err.name = "FunctionError";
  }
  return err;
}

// runXFailure is the envelope a failed nested call posts back to the worker.
export function runXFailure(err: unknown): RunXFailure {
  const { name, issues, status } = (err ?? {}) as { name?: string; issues?: unknown; status?: unknown };
  return {
    ok: false,
    error: String((err instanceof Error ? err.message : err) ?? "runX error"),
    ...(name ? { errorName: name } : {}),
    ...(issues ? { issues } : {}),
    ...(typeof status === "number" ? { status } : {}),
  };
}
