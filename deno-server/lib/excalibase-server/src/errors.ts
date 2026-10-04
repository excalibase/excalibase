/**
 * SQLSTATE values that the runtime treats as retryable conflicts.
 *  - 40001: serialization_failure (raised under SERIALIZABLE/REPEATABLE READ
 *    when transactions can't be ordered without a real conflict).
 *  - 40P01: deadlock_detected (raised when the deadlock detector kills one
 *    side of a cycle).
 *
 * Both surface through `ConflictError.lastSqlState` after the retry loop
 * exhausts. The HTTP layer maps `code === "MUTATION_CONFLICT"` → 409.
 */
export type ConflictSqlState = "40001" | "40P01";

export interface ConflictErrorInit {
  /** Human-readable summary; typically includes the attempt count. */
  readonly message: string;
  /** Number of attempts that ran before the retry loop gave up. */
  readonly attempts: number;
  /** SQLSTATE of the final failing attempt — `40001` or `40P01`. */
  readonly lastSqlState: ConflictSqlState;
  /** Postgres error message from the final failing attempt. */
  readonly lastMessage: string;
}

/**
 * Thrown when a mutation's automatic retry loop exhausts its max attempts
 * on a serialization or deadlock conflict (`40001` / `40P01`).
 *
 * Actions do NOT retry — they propagate this error to the caller if a
 * nested ctx.runMutation hits it.
 */
export class ConflictError extends Error {
  /** Stable identifier — clients match on this, NOT on the message text. */
  readonly code: "MUTATION_CONFLICT" = "MUTATION_CONFLICT";
  readonly attempts: number;
  readonly lastSqlState: ConflictSqlState;
  readonly lastMessage: string;

  constructor(init: ConflictErrorInit) {
    super(init.message);
    this.name = "ConflictError";
    this.attempts = init.attempts;
    this.lastSqlState = init.lastSqlState;
    this.lastMessage = init.lastMessage;
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/** A client-error status a function may refuse a caller with. */
export type FunctionErrorStatus =
  | 400 | 401 | 402 | 403 | 404 | 405 | 406 | 407 | 408 | 409
  | 410 | 411 | 412 | 413 | 414 | 415 | 416 | 417 | 418 | 421
  | 422 | 423 | 424 | 425 | 426 | 428 | 429 | 431 | 451 | 499;

/**
 * Thrown by a handler to refuse its caller: the runtime answers with `status`
 * and `{ error: message }`. Any other thrown error is a crash: the caller gets
 * a bare 500 and the message goes to the function's logs.
 *
 *   if (!ctx.auth.claims) throw new FunctionError(401, "sign in first");
 *   if (ctx.auth.claims.role !== "staff") throw new FunctionError(403, "staff only");
 */
export class FunctionError extends Error {
  readonly status: FunctionErrorStatus;

  constructor(status: FunctionErrorStatus, message: string) {
    if (!Number.isInteger(status) || status < 400 || status > 499) {
      throw new RangeError(`FunctionError status must be a 4xx client error, got ${status}`);
    }
    super(message);
    this.name = "FunctionError";
    this.status = status;
    Object.setPrototypeOf(this, new.target.prototype);
  }
}
