// Mutation scopes hold a mutation's ctx.scheduler calls until it succeeds
// (EXC-625). A top-level mutation owns a scope. A nested ctx.runMutation gets
// a child scope whose calls join its parent's only when it succeeds. A ref
// that names no live scope of the caller's project takes no calls.

interface Scope<Op> {
  parentRef?: string;
  projectId: string;
  ops: Op[];
}

export interface ClosedScope<Op> {
  /** Calls to send now: a top-level mutation that succeeded. */
  send: Op[];
  /** A nested mutation succeeded after its caller ended, so its calls are lost. */
  lost?: true;
}

/** How a closed scope's calls ended: sent (or none), lost, or refused by provisioning. */
export type Settled = "ok" | "lost" | "unrecorded";

export class MutationScopes<Op = unknown> {
  private readonly scopes = new Map<string, Scope<Op>>();

  /**
   * open starts the scope for one invocation and returns its ref, or "" when
   * the invocation opens none (anything but a mutation, with no live parent).
   */
  open(kind: string, parentRef: string, projectId: string): string {
    const parentLive = parentRef !== "" && this.live(parentRef, projectId) !== undefined;
    if (!parentLive && kind !== "mutation") return "";
    // Unguessable, so a function cannot name another invocation's scope.
    const ref = `t${crypto.randomUUID()}`;
    this.scopes.set(ref, { parentRef: parentLive ? parentRef : undefined, projectId, ops: [] });
    return ref;
  }

  /** add holds a call in a live scope; false when the ref names none. */
  add(ref: string, projectId: string, op: Op): boolean {
    const scope = this.live(ref, projectId);
    if (!scope) return false;
    scope.ops = [...scope.ops, op];
    return true;
  }

  /** hold is add for a worker's call: a ref that names no live scope throws. */
  hold(ref: string, projectId: string, op: Op): void {
    if (!this.add(ref, projectId, op)) {
      throw new Error("ctx.scheduler: the mutation that made this call has already ended");
    }
  }

  /**
   * settle closes a scope and sends what a succeeded top-level mutation
   * scheduled. Failures are logged under `label` (the function id); the
   * send error can name internal endpoints, so it never reaches the result.
   */
  async settle(
    ref: string,
    settled: "success" | "error",
    send: (ops: Op[]) => Promise<void>,
    label = "",
  ): Promise<Settled> {
    const closed = this.close(ref, settled);
    if (closed.lost) {
      console.warn(`[runtime] ${label} succeeded after its calling mutation ended; its scheduled functions were dropped`);
      return "lost";
    }
    if (closed.send.length === 0) return "ok";
    try {
      await send(closed.send);
      return "ok";
    } catch (sendErr) {
      const reason = sendErr instanceof Error ? sendErr.message : String(sendErr);
      console.warn(`[runtime] ${label} succeeded but its scheduled functions were not recorded:`, reason);
      return "unrecorded";
    }
  }

  /** close ends a scope and says which of its calls to send now. */
  close(ref: string, settled: "success" | "error"): ClosedScope<Op> {
    const scope = ref ? this.scopes.get(ref) : undefined;
    if (!scope) return { send: [] };
    this.scopes.delete(ref);
    if (settled !== "success" || scope.ops.length === 0) return { send: [] };
    if (scope.parentRef === undefined) return { send: scope.ops };
    const parent = this.scopes.get(scope.parentRef);
    if (!parent) return { send: [], lost: true };
    parent.ops = [...parent.ops, ...scope.ops];
    return { send: [] };
  }

  private live(ref: string, projectId: string): Scope<Op> | undefined {
    const scope = this.scopes.get(ref);
    return scope && scope.projectId === projectId ? scope : undefined;
  }
}
