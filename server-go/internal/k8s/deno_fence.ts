// Runs before the function runtime starts (EXC-622): a policy controller may
// program the pod's egress fence after the pod starts, so this waits until the
// Kubernetes API, which the fence always denies, is refused while DNS, which
// it always allows, answers. Run with `deno eval`, which grants net and env.

export type Address = [string, number];

export interface FenceCheck {
  canary: Address;
  dns: Address;
  deadlineMillis: number;
  answers: (address: Address) => Promise<boolean>;
  pause: () => Promise<void>;
  now: () => number;
}

export interface FenceResult {
  fenced: boolean;
  message: string;
}

const dialMillis = 500;
const pauseMillis = 200;

export function parseAddress(host: string | undefined, port: string | undefined): Address {
  const number = Number(port);
  if (!host || !Number.isInteger(number) || number < 1 || number > 65535) {
    throw new Error(`egress fence check: ${host}:${port} is not an address`);
  }
  return [host.replace(/^\[|\]$/g, ""), number];
}

// nameserver is the override (host:port) or the first nameserver in resolv.conf on port 53.
export function nameserver(resolvConf: string, override?: string): Address {
  if (override) {
    const at = override.lastIndexOf(":");
    return parseAddress(override.slice(0, at), override.slice(at + 1));
  }
  return parseAddress(/^nameserver\s+(\S+)/m.exec(resolvConf)?.[1], "53");
}

// answers is whether a TCP connection opens within the timeout; a late one is dropped.
export function answers([hostname, port]: Address, timeoutMillis = dialMillis): Promise<boolean> {
  return new Promise((resolve) => {
    const timer = setTimeout(() => resolve(false), timeoutMillis);
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

export async function waitForFence(check: FenceCheck, start = check.now()): Promise<FenceResult> {
  const [canaryAnswered, dnsAnswered] = await Promise.all([check.answers(check.canary), check.answers(check.dns)]);
  const elapsed = check.now() - start;
  if (!canaryAnswered && dnsAnswered) {
    return { fenced: true, message: `egress fence is in place after ${elapsed} ms` };
  }
  if (elapsed >= check.deadlineMillis) {
    const why = canaryAnswered
      ? `${check.canary.join(":")} (the Kubernetes API) still answers`
      : `DNS at ${check.dns.join(":")} never answered`;
    return {
      fenced: false,
      message: `egress fence is not in place after ${check.deadlineMillis} ms: ${why}; the runtime will not start`,
    };
  }
  await check.pause();
  return waitForFence(check, start);
}

// main reads the pod's environment and reports; its exit code is 0 only once fenced.
export async function main(
  env: { get(name: string): string | undefined },
  readResolvConf: () => Promise<string>,
  log: (line: string) => void,
): Promise<number> {
  const dnsOverride = env.get("FENCE_DNS");
  const result = await waitForFence({
    canary: parseAddress(env.get("KUBERNETES_SERVICE_HOST"), env.get("KUBERNETES_SERVICE_PORT")),
    dns: nameserver(dnsOverride ? "" : await readResolvConf(), dnsOverride),
    deadlineMillis: Number(env.get("FENCE_DEADLINE_MS") ?? "30000"),
    answers,
    pause: () => new Promise((resolve) => setTimeout(resolve, pauseMillis)),
    now: Date.now,
  });
  log(result.message);
  return result.fenced ? 0 : 1;
}

if (import.meta.main) {
  // Exit at once: a dial still pending on a dropped packet must not hold the pod.
  const readResolvConf = () => Deno.readTextFile("/etc/resolv.conf");
  Deno.exit(await main(Deno.env, readResolvConf, console.log).catch((error) => (console.log(String(error)), 2)));
}
