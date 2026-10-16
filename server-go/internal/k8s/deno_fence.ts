// Runs before the function runtime starts (EXC-622): a policy controller may
// program the pod's egress fence after the pod starts, so this waits until the
// Kubernetes API, which the fence always denies, is refused while DNS, which
// it always allows, answers. Run with `deno eval`, which grants net and env.
const dialMillis = 500;
const pauseMillis = 200;
const deadlineMillis = Number(Deno.env.get("FENCE_DEADLINE_MS") ?? "30000");

function address(host: string | undefined, port: string | undefined): [string, number] {
  if (!host || !port) {
    console.error("egress fence check: KUBERNETES_SERVICE_HOST/PORT are not set");
    Deno.exit(2);
  }
  return [host.replace(/^\[|\]$/g, ""), Number(port)];
}

async function nameserver(): Promise<[string, number]> {
  const override = Deno.env.get("FENCE_DNS");
  if (override) {
    const at = override.lastIndexOf(":");
    return address(override.slice(0, at), override.slice(at + 1));
  }
  const resolv = await Deno.readTextFile("/etc/resolv.conf");
  return address(/^nameserver\s+(\S+)/m.exec(resolv)?.[1], "53");
}

// answers is whether a TCP connection opens within dialMillis; a late one is dropped.
function answers([hostname, port]: [string, number]): Promise<boolean> {
  return new Promise((resolve) => {
    const timer = setTimeout(() => resolve(false), dialMillis);
    Deno.connect({ hostname, port }).then(
      (conn) => {
        conn.close();
        clearTimeout(timer);
        resolve(true);
      },
      () => {
        clearTimeout(timer);
        resolve(false);
      },
    );
  });
}

const canary = address(Deno.env.get("KUBERNETES_SERVICE_HOST"), Deno.env.get("KUBERNETES_SERVICE_PORT"));
const dns = await nameserver();
const start = Date.now();
let canaryAnswered = false;
let dnsAnswered = false;
while (Date.now() - start < deadlineMillis) {
  [canaryAnswered, dnsAnswered] = await Promise.all([answers(canary), answers(dns)]);
  if (!canaryAnswered && dnsAnswered) {
    console.log(`egress fence is in place after ${Date.now() - start} ms`);
    Deno.exit(0);
  }
  await new Promise((resolve) => setTimeout(resolve, pauseMillis));
}
const why = canaryAnswered
  ? `${canary.join(":")} (the Kubernetes API) still answers`
  : `DNS at ${dns.join(":")} never answered`;
console.error(`egress fence is not in place after ${deadlineMillis} ms: ${why}; the runtime will not start`);
Deno.exit(1);
