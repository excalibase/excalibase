import { ConflictError } from "../src";

describe("ConflictError", () => {
  it("is an Error subclass carrying retry context", () => {
    const err = new ConflictError({
      message: "mutation failed after 5 attempts",
      attempts: 5,
      lastSqlState: "40001",
      lastMessage: "could not serialize access",
    });
    expect(err).toBeInstanceOf(Error);
    expect(err.name).toBe("ConflictError");
    expect(err.message).toBe("mutation failed after 5 attempts");
    expect(err.code).toBe("MUTATION_CONFLICT");
    expect(err.attempts).toBe(5);
    expect(err.lastSqlState).toBe("40001");
    expect(err.lastMessage).toBe("could not serialize access");
  });

  it("accepts the deadlock SQLSTATE 40P01 as a valid lastSqlState", () => {
    const err = new ConflictError({
      message: "deadlock retries exhausted",
      attempts: 3,
      lastSqlState: "40P01",
      lastMessage: "deadlock detected",
    });
    expect(err.lastSqlState).toBe("40P01");
  });

  it("preserves the code string verbatim — callers/HTTP layer match on it", () => {
    const err = new ConflictError({
      message: "",
      attempts: 1,
      lastSqlState: "40001",
      lastMessage: "x",
    });
    // Public contract: HTTP layer maps `code === "MUTATION_CONFLICT"` → 409.
    expect(err.code).toBe("MUTATION_CONFLICT");
  });

  it("survives instanceof across the same realm (prototype is restored)", () => {
    // ts/jest sometimes loses prototype chain on classes that extend Error
    // when target is below ES2015; this guards the explicit setPrototypeOf.
    const err = new ConflictError({
      message: "x",
      attempts: 1,
      lastSqlState: "40001",
      lastMessage: "y",
    });
    expect(err instanceof ConflictError).toBe(true);
    expect(err instanceof Error).toBe(true);
  });

  it("attempts and lastSqlState are read-only at the type boundary", () => {
    const err = new ConflictError({
      message: "x",
      attempts: 2,
      lastSqlState: "40001",
      lastMessage: "y",
    });
    // Runtime check that the field is the value we set; the readonly
    // modifier is enforced by TS only — the runtime test asserts no
    // unexpected mutation/coercion happens in the constructor.
    expect(err.attempts).toBe(2);
    expect(err.lastSqlState).toBe("40001");
  });
});
