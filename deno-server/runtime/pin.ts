// BYOC address pinning (EXC-359).
//
// The runtime cannot dial an external database through provisioning's SSRF
// guard, so provisioning resolves the BYOC host itself, validates every
// answer, and hands the runtime a DSN whose authority is the validated IP
// literal plus the original hostname (EXCALIBASE_DB_HOST) for TLS SNI. The
// deploy carries BYOC_PINNED=1; this module is the runtime side of that
// contract: an unpinned BYOC DSN is refused, the worker's net grant is the
// pinned ip:port plus the project's egress allowlist (EXC-348) and nothing
// else, and the pool keeps the hostname for TLS.

export interface PinnedTarget {
  host: string;
  port: number;
  /** Deno net-permission form: `ip:port`, IPv6 bracketed. */
  hostPort: string;
}

const DEFAULT_PG_PORT = 5432;
const IPV4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;

function isIPv4(host: string): boolean {
  const match = IPV4.exec(host);
  return match !== null && match.slice(1).every((octet) => Number(octet) <= 255);
}

function isBracketedIPv6(host: string): boolean {
  if (!host.startsWith("[") || !host.endsWith("]")) return false;
  const inner = host.slice(1, -1);
  return inner.includes(":") && /^[0-9a-fA-F:.]+$/.test(inner);
}

/** Raised when a BYOC deploy or pool target violates the pin contract. */
export class PinError extends Error {
  constructor(reason: string) {
    super(`BYOC database url is not pinned: ${reason}`);
    this.name = "PinError";
  }
}

function notPinned(reason: string): Error {
  return new PinError(reason);
}

/**
 * Parse a pinned DSN. Throws unless the authority is a single IP literal:
 * a hostname would let the runtime resolve DNS on its own, which is exactly
 * the rebind window the pin closes.
 */
export function pinnedTarget(url: string): PinnedTarget {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    throw notPinned("malformed url");
  }
  const host = parsed.hostname;
  if (host.includes(",")) throw notPinned("multi-host authority");
  if (!isIPv4(host) && !isBracketedIPv6(host)) throw notPinned("authority must be an IP literal");
  const port = parsed.port === "" ? DEFAULT_PG_PORT : Number(parsed.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw notPinned("invalid port");
  const bare = isBracketedIPv6(host) ? host.slice(1, -1) : host;
  return { host: bare, port, hostPort: `${host}:${port}` };
}

export function isPinned(secrets: Record<string, string>): boolean {
  return secrets.BYOC_PINNED === "1";
}

/**
 * The worker's `net` permission. `allowedHosts` is the project's egress
 * allowlist (EXC-348). A pinned deploy additionally reaches the pinned
 * ip:port — the database address is never taken from the DSN's hostname.
 * `false` (no network) when there is nothing to grant.
 */
export function workerNetGrant(
  secrets: Record<string, string>,
  allowedHosts: string[],
): string[] | false {
  if (!isPinned(secrets)) return allowedHosts.length > 0 ? allowedHosts : false;
  const url = secrets.EXCALIBASE_DB_URL;
  if (!url) throw notPinned("EXCALIBASE_DB_URL missing");
  const pinned = pinnedTarget(url).hostPort;
  return [pinned, ...allowedHosts.filter((host) => host !== pinned)];
}

export interface PoolTarget {
  url: string;
  pinned: boolean;
  hostName?: string;
}

/**
 * postgres.js options for the main-thread pool. When the process env says the
 * database is pinned, the url must be an IP literal and the hostname is sent
 * as TLS servername, so SNI and any certificate check still see the name
 * (postgres.js omits servername for IP hosts unless `ssl` supplies it).
 * `sslmode=require` keeps its postgres.js meaning (encrypt, no verification).
 */
export function poolConnectOptions(target: PoolTarget): { ssl?: Record<string, unknown> } {
  if (!target.pinned) return {};
  const parsed = new URL(target.url);
  pinnedTarget(target.url);
  if (!target.hostName) throw notPinned("EXCALIBASE_DB_HOST missing");
  const sslmode = parsed.searchParams.get("sslmode") ?? "require";
  const ssl = sslmode === "verify-full"
    ? { servername: target.hostName }
    : { rejectUnauthorized: false, servername: target.hostName };
  return { ssl };
}
