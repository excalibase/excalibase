// EXC-640: deploys refused for their code, kept where /logs/{id} reads them
// when the runtime holds no version of the function.

export interface RefusalEntry {
  level: string;
  msg: string;
  ts: number;
}

export class DeployRefusals {
  private readonly byId = new Map<string, RefusalEntry[]>();

  constructor(private readonly capacity: number, private readonly maxLine: number) {}

  /** The log line a refused deploy leaves. */
  entry(message: string, now = Date.now()): RefusalEntry {
    return { level: "error", msg: `deploy refused: ${message}`.slice(0, this.maxLine), ts: now };
  }

  /** Remembers the refusal of a function the runtime does not hold, dropping the oldest past capacity. */
  record(id: string, entry: RefusalEntry): void {
    this.byId.delete(id);
    this.byId.set(id, [entry]);
    if (this.byId.size > this.capacity) {
      const oldest = this.byId.keys().next().value;
      if (oldest !== undefined) this.byId.delete(oldest);
    }
  }

  /** The refusal logged for id after sinceMs, or null when there is none. */
  logs(id: string, sinceMs?: number): RefusalEntry[] | null {
    const entries = this.byId.get(id);
    if (!entries) return null;
    return typeof sinceMs === "number" && Number.isFinite(sinceMs) ? entries.filter((e) => e.ts > sinceMs) : entries.slice();
  }

  /** Forgets id's refusal; true when there was one. */
  clear(id: string): boolean {
    return this.byId.delete(id);
  }
}

/** Appends entry to a function's log ring, keeping the newest size entries. */
export function appendLog(logs: RefusalEntry[], entry: RefusalEntry, size: number): void {
  logs.push(entry);
  if (logs.length > size) logs.splice(0, logs.length - size);
}
