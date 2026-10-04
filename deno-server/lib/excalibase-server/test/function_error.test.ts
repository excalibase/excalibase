import { FunctionError } from "../src";

describe("FunctionError", () => {
  it("carries the status the runtime answers with", () => {
    const err = new FunctionError(403, "only staff may upload");
    expect(err).toBeInstanceOf(Error);
    expect(err.name).toBe("FunctionError");
    expect(err.status).toBe(403);
    expect(err.message).toBe("only staff may upload");
  });

  it("accepts every client-error status", () => {
    for (const status of [400, 401, 404, 409, 422, 429, 499] as const) {
      expect(new FunctionError(status, "refused").status).toBe(status);
    }
  });

  it("refuses a status that is not a client error, since a crash is not a refusal", () => {
    for (const status of [200, 302, 399, 500, 503, 403.5, Number.NaN]) {
      expect(() => new FunctionError(status as 400, "x")).toThrow(RangeError);
    }
  });
});
