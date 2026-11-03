import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { handlerContractRefusal } from "../runtime/handler_contract.ts";
import { appendLog, DeployRefusals } from "../runtime/refusals.ts";

const contract = "index.ts must use export default (req: Request) => Response; ";

Deno.test("a handler or a v2 export is accepted", () => {
  assertEquals(handlerContractRefusal(() => new Response(), ["default"], false), null);
  assertEquals(handlerContractRefusal({ kind: "query" }, ["default"], true), null);
});

Deno.test("the refusal names what the module exports instead", () => {
  assertEquals(handlerContractRefusal(undefined, ["invite", "helper"], false), contract + "found only named exports: invite, helper");
  assertEquals(handlerContractRefusal(undefined, [], false), contract + "found no exports");
  assertEquals(handlerContractRefusal({ greet: 1 }, ["default"], false), contract + "the default export is a object, not a function");
  assertEquals(handlerContractRefusal(null, ["default"], false), contract + "the default export is a null, not a function");
});

Deno.test("a refusal is readable until cleared, and the oldest goes past capacity", () => {
  const refusals = new DeployRefusals(2, 40);
  refusals.record("a", refusals.entry("x".repeat(100), 10));
  assertEquals(refusals.logs("a"), [{ level: "error", msg: "deploy refused: " + "x".repeat(24), ts: 10 }]);
  assertEquals(refusals.logs("a", 10), []);
  refusals.record("b", refusals.entry("b", 11));
  refusals.record("c", refusals.entry("c", 12));
  assertEquals(refusals.logs("a"), null);
  assertEquals(refusals.clear("b"), true);
  assertEquals(refusals.clear("b"), false);
  assertEquals(refusals.logs("c")?.length, 1);
});

Deno.test("a log ring keeps the newest entries", () => {
  const logs = [{ level: "log", msg: "1", ts: 1 }, { level: "log", msg: "2", ts: 2 }];
  appendLog(logs, { level: "error", msg: "3", ts: 3 }, 2);
  assertEquals(logs.map((l) => l.msg), ["2", "3"]);
});
