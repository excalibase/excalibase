import { api } from '../api/client';
import type { DatabaseInstance } from '../types';

// EXC-473: a pause (backup first), a resume and the stop before a deletion's
// grace period take minutes. Studio asks the server not to wait for them and
// reads the project until it shows the outcome.

export const RESPOND_ASYNC = { headers: { Prefer: 'respond-async' } };
export const PROJECT_FOLLOW_MS = 3000;
// The server bounds a pause and a resume itself; this only stops the page waiting.
const FOLLOW_LIMIT_MS = 20 * 60 * 1000;

// A project operation the server reports as not completed, in its words.
export class ProjectOperationError extends Error {}

export type ProjectOperation = 'pause' | 'resume' | 'deletion';

const DONE: Record<ProjectOperation, string> = {
  pause: 'PAUSED',
  resume: 'ACTIVE',
  deletion: 'PENDING_DELETION',
};

// The failure is cleared when the operation starts, so one seen while
// following belongs to it.
function outcome(operation: ProjectOperation, project: DatabaseInstance): 'pending' | 'done' {
  if (project.status === DONE[operation]) return 'done';
  if (project.failureReason) throw new ProjectOperationError(project.failureReason);
  return 'pending';
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

export async function followProject(
  projectId: string,
  operation: ProjectOperation,
  intervalMs: number,
): Promise<DatabaseInstance> {
  const deadline = Date.now() + FOLLOW_LIMIT_MS;
  while (Date.now() < deadline) {
    const { data } = await api.get<DatabaseInstance>(`/provision/${projectId}`);
    if (outcome(operation, data) === 'done') return data;
    await sleep(intervalMs);
  }
  throw new ProjectOperationError(
    `The ${operation} has not finished after 20 minutes; the project's status shows where it is.`,
  );
}

// Follows an operation the server accepted (202); an answer that already
// carries the outcome is returned as it is.
export async function settleProject(
  projectId: string,
  operation: ProjectOperation,
  response: { status: number; data: { status?: string } },
  intervalMs: number,
): Promise<{ status?: string }> {
  if (response.status !== 202 || response.data.status === DONE[operation]) return response.data;
  return followProject(projectId, operation, intervalMs);
}

// The words to show for a failed operation: the server's refusal or the
// failure it recorded.
export function projectOperationMessage(err: unknown, fallback: string): string {
  if (err instanceof ProjectOperationError) return err.message;
  const message = (err as { response?: { data?: { error?: unknown } } } | null)?.response?.data?.error;
  return typeof message === 'string' && message.trim() !== '' ? message : fallback;
}
