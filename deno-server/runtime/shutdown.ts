// EXC-569: how the runtime stops. Kubernetes signals the pod and takes it out
// of the Service at the same time; until every node has seen the change,
// calls still arrive. Exiting at once refused them, so the runtime keeps
// serving for a drain period (reporting "draining" on /health so readiness
// drops it sooner), then stops the listener, closes the pool and exits.

const DEFAULT_DRAIN_MS = 5000;
// Below Kubernetes' default 30s termination grace, so the runtime always
// exits on its own before it is killed.
const MAX_DRAIN_MS = 20000;

export function shutdownDrainMs(raw: string | undefined): number {
  if (raw === undefined || raw.trim() === "") return DEFAULT_DRAIN_MS;
  const ms = Number(raw);
  if (!Number.isFinite(ms) || ms < 0) return DEFAULT_DRAIN_MS;
  return Math.min(ms, MAX_DRAIN_MS);
}

export interface DrainSteps {
  drainMs: number;
  markDraining: () => void;
  sleep: (ms: number) => Promise<void>;
  stopServing: () => Promise<void>;
  closePool: () => Promise<void>;
  exit: (code: number) => void;
}

// drainThenExit returns the signal handler. A second signal joins the
// shutdown already under way instead of starting another.
export function drainThenExit(steps: DrainSteps): () => Promise<void> {
  let running: Promise<void> | null = null;
  const run = async () => {
    steps.markDraining();
    await steps.sleep(steps.drainMs);
    try { await steps.stopServing(); } catch (_) { /* already stopped */ }
    try { await steps.closePool(); } catch (_) { /* postgres closes the sockets */ }
    steps.exit(0);
  };
  return () => {
    running ??= run();
    return running;
  };
}
