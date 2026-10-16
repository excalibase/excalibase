// The function runtime's wait-for-fence step (EXC-622). The script lives with
// the renderer that embeds it (server-go/internal/k8s) and runs as
// `deno eval` in the runtime image before the runtime starts.

import { assert, assertEquals, assertStringIncludes, assertThrows } from "https://deno.land/std@0.224.0/assert/mod.ts";
import {
  type Address,
  answers,
  type FenceCheck,
  main,
  nameserver,
  parseAddress,
  waitForFence,
} from "../../server-go/internal/k8s/deno_fence.ts";

const canary: Address = ["10.43.0.1", 443];
const dns: Address = ["10.43.0.10", 53];

// fakeCheck answers from a script of rounds and moves a fake clock 700 ms per round.
function fakeCheck(
  rounds: Array<{ canary: boolean; dns: boolean }>,
  deadlineMillis = 2000,
): FenceCheck & { pauses: number } {
  let clock = 0;
  let round = 0;
  const check = {
    canary,
    dns,
    deadlineMillis,
    pauses: 0,
    answers: (address: Address) => {
      const current = rounds[Math.min(round, rounds.length - 1)];
      return Promise.resolve(address === canary ? current.canary : current.dns);
    },
    pause: () => {
      check.pauses++;
      round++;
      clock += 700;
      return Promise.resolve();
    },
    now: () => clock,
  };
  return check;
}

Deno.test("the fence is in place once the canary is refused and DNS answers", async () => {
  const check = fakeCheck([{ canary: true, dns: true }, { canary: false, dns: true }]);
  const result = await waitForFence(check);
  assert(result.fenced);
  assertEquals(check.pauses, 1);
  assertStringIncludes(result.message, "egress fence is in place after 700 ms");
});

Deno.test("a canary that keeps answering fails at the deadline, naming it", async () => {
  const result = await waitForFence(fakeCheck([{ canary: true, dns: true }]));
  assert(!result.fenced);
  assertStringIncludes(result.message, "not in place after 2000 ms");
  assertStringIncludes(result.message, "10.43.0.1:443 (the Kubernetes API) still answers");
});

Deno.test("a refused canary proves nothing while DNS is down", async () => {
  const result = await waitForFence(fakeCheck([{ canary: false, dns: false }]));
  assert(!result.fenced);
  assertStringIncludes(result.message, "DNS at 10.43.0.10:53 never answered");
});

Deno.test("addresses are checked", () => {
  assertEquals(parseAddress("[fd00::1]", "443"), ["fd00::1", 443]);
  assertThrows(() => parseAddress(undefined, "443"));
  assertThrows(() => parseAddress("10.43.0.1", "0"));
  assertThrows(() => parseAddress("10.43.0.1", "http"));
});

Deno.test("the nameserver comes from resolv.conf unless overridden", () => {
  assertEquals(nameserver("search svc.cluster.local\nnameserver 10.43.0.10\nnameserver 1.1.1.1\n"), ["10.43.0.10", 53]);
  assertEquals(nameserver("", "127.0.0.1:5353"), ["127.0.0.1", 5353]);
  assertThrows(() => nameserver("search only\n"));
});

Deno.test("answers dials for real: a listener answers, a closed port does not", async () => {
  const listener = Deno.listen({ hostname: "127.0.0.1", port: 0 });
  const port = (listener.addr as Deno.NetAddr).port;
  const accepting = (async () => {
    for await (const conn of listener) conn.close();
  })();
  assert(await answers(["127.0.0.1", port]));
  listener.close();
  await accepting.catch(() => {});
  assert(!(await answers(["127.0.0.1", port])));
});

Deno.test("main reads the pod's environment and exits 0 only once fenced", async () => {
  const listener = Deno.listen({ hostname: "127.0.0.1", port: 0 });
  const dnsPort = (listener.addr as Deno.NetAddr).port;
  const accepting = (async () => {
    for await (const conn of listener) conn.close();
  })();
  const closed = Deno.listen({ hostname: "127.0.0.1", port: 0 });
  const closedPort = (closed.addr as Deno.NetAddr).port;
  closed.close();
  const lines: string[] = [];
  const env = (values: Record<string, string>) => ({ get: (name: string) => values[name] });
  const fenced = await main(
    env({
      KUBERNETES_SERVICE_HOST: "127.0.0.1",
      KUBERNETES_SERVICE_PORT: String(closedPort),
      FENCE_DEADLINE_MS: "300",
    }),
    () => Promise.resolve(`nameserver 127.0.0.1\n`),
    (line) => lines.push(line),
  );
  // resolv.conf names port 53, where nothing listens here: DNS never answers.
  assertEquals(fenced, 1);
  const open = await main(
    env({
      KUBERNETES_SERVICE_HOST: "127.0.0.1",
      KUBERNETES_SERVICE_PORT: String(closedPort),
      FENCE_DNS: `127.0.0.1:${dnsPort}`,
      FENCE_DEADLINE_MS: "300",
    }),
    () => Promise.reject(new Error("resolv.conf must not be read with an override")),
    (line) => lines.push(line),
  );
  assertEquals(open, 0);
  assertStringIncludes(lines[lines.length - 1], "egress fence is in place");
  listener.close();
  await accepting.catch(() => {});
});
