import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { runXFailure, runXTargetError, withoutCrashDetail } from "../runtime/invoke_errors.ts";

const json = { "content-type": "application/json" };

Deno.test("a crash reaches the caller of /invoke as a bare 500", () => {
  const crashed = { status: 500, headers: json, body: '{"error":"relation secret does not exist"}', failed: true };
  assertEquals(withoutCrashDetail(crashed), { status: 500, headers: json, body: '{"error":"internal error"}' });
});

Deno.test("a refusal or a success passes through unchanged", () => {
  const refused = { status: 403, headers: json, body: '{"error":"staff only"}' };
  assertEquals(withoutCrashDetail(refused), refused);
  const ok = { status: 200, headers: json, body: '{"data":1}' };
  assertEquals(withoutCrashDetail(ok), ok);
});

Deno.test("a nested target's refusal is rethrown as a FunctionError with its status", () => {
  const err = runXTargetError(403, { error: "staff only" }) as Error & { status?: number };
  assertEquals([err.name, err.message, err.status], ["FunctionError", "staff only", 403]);
});

Deno.test("a nested target's validation failure keeps its issues", () => {
  const issues = [{ path: ["x"], message: "Required" }];
  const err = runXTargetError(400, { error: "validation", issues }) as Error & { issues?: unknown };
  assertEquals([err.name, err.issues], ["ValidationError", issues]);
});

Deno.test("a nested target's crash keeps its message for the calling function", () => {
  const err = runXTargetError(500, { error: "nope-inner" }) as Error & { status?: number };
  assertEquals([err.name, err.message, err.status], ["Error", "nope-inner", undefined]);
  assertEquals(runXTargetError(502, {}).message, "runX target returned 502");
});

Deno.test("a failed nested call posts its name, issues and status back to the worker", () => {
  const refusal = Object.assign(new Error("staff only"), { name: "FunctionError", status: 403 });
  assertEquals(runXFailure(refusal), { ok: false, error: "staff only", errorName: "FunctionError", status: 403 });
  const invalid = Object.assign(new Error("validation"), { name: "ValidationError", issues: [1] });
  assertEquals(runXFailure(invalid), { ok: false, error: "validation", errorName: "ValidationError", issues: [1] });
  assertEquals(runXFailure("depth exceeded"), { ok: false, error: "depth exceeded" });
});
