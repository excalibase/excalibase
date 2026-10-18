// EXC-625 — mutation scopes hold a mutation's ctx.scheduler calls until it
// succeeds, now that no database transaction does.

import { assertEquals, assertNotEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { MutationScopes } from "../runtime/mutation_scopes.ts";

const op = (name: string) => ({ op: "cancel" as const, id: name });

Deno.test("a top-level mutation's calls are handed back once it succeeds", () => {
  const scopes = new MutationScopes();
  const ref = scopes.open("mutation", "", "proj_a");
  assertEquals(scopes.add(ref, "proj_a", op("one")), true);
  assertEquals(scopes.add(ref, "proj_a", op("two")), true);
  assertEquals(scopes.close(ref, "success"), { send: [op("one"), op("two")] });
  assertEquals(scopes.add(ref, "proj_a", op("late")), false, "an ended scope takes no more calls");
});

Deno.test("a failed mutation's calls are dropped", () => {
  const scopes = new MutationScopes();
  const ref = scopes.open("mutation", "", "proj_a");
  scopes.add(ref, "proj_a", op("one"));
  assertEquals(scopes.close(ref, "error"), { send: [] });
});

Deno.test("only mutations open a scope", () => {
  const scopes = new MutationScopes();
  for (const kind of ["query", "action", "httpAction", ""]) {
    assertEquals(scopes.open(kind, "", "proj_a"), "");
  }
  assertEquals(scopes.close("", "success"), { send: [] });
});

Deno.test("a nested mutation's calls join its caller only when it succeeds", () => {
  const scopes = new MutationScopes();
  const outer = scopes.open("mutation", "", "proj_a");
  const good = scopes.open("mutation", outer, "proj_a");
  scopes.add(good, "proj_a", op("kept"));
  assertEquals(scopes.close(good, "success"), { send: [] });
  const bad = scopes.open("mutation", outer, "proj_a");
  scopes.add(bad, "proj_a", op("dropped"));
  assertEquals(scopes.close(bad, "error"), { send: [] });
  assertEquals(scopes.close(outer, "success"), { send: [op("kept")] });
});

Deno.test("a nested mutation that outlives its caller reports its calls lost", () => {
  const scopes = new MutationScopes();
  const outer = scopes.open("mutation", "", "proj_a");
  const inner = scopes.open("mutation", outer, "proj_a");
  scopes.add(inner, "proj_a", op("orphan"));
  scopes.close(outer, "error");
  assertEquals(scopes.close(inner, "success"), { send: [], lost: true });
});

Deno.test("refs are unguessable and bound to their project", () => {
  const scopes = new MutationScopes();
  const a = scopes.open("mutation", "", "proj_a");
  const b = scopes.open("mutation", "", "proj_a");
  assertNotEquals(a, b);
  assertEquals(a.length > 30, true);
  assertEquals(scopes.add(a, "proj_b", op("x")), false, "another project cannot add to the scope");
  assertEquals(scopes.add("t-unknown", "proj_a", op("x")), false);
  // A nested call naming another project's scope opens a top level of its own.
  const foreign = scopes.open("mutation", a, "proj_b");
  scopes.add(foreign, "proj_b", op("own"));
  assertEquals(scopes.close(foreign, "success"), { send: [op("own")] });
  assertEquals(scopes.close(a, "success"), { send: [] });
});
