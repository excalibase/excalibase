import type { QueryClient } from '@tanstack/react-query';
import { serverErrorMessage } from '../../../utils/serverError';
import { parseInstant } from '../../../utils/instant';

const DASH = '—';

// Usage that could not be read stays a dash: a zero would read as an idle project.
export const formatCpu = (cores?: number) => (cores == null ? DASH : cores.toFixed(2));
export const formatMemory = (bytes?: number) => (bytes == null ? DASH : `${(bytes / 1024 / 1024).toFixed(0)} MiB`);

// The server sends a created time as text; an older shape wraps it in an object.
export function createdOn(value: string | { time: string } | undefined): string {
  const raw = typeof value === 'object' ? value?.time : value;
  const parsed = raw ? new Date(parseInstant(raw)) : null;
  return parsed && !Number.isNaN(parsed.getTime()) ? parsed.toLocaleDateString() : DASH;
}

// Platform staff accounts are not suspended from the list.
export const suspendableUsers = <T extends { role: string }>(users: readonly T[]): T[] => users.filter((user) => !user.role.startsWith('platform_'));

// Suspending the owner of several selected projects is one suspension.
export const distinctOwners = (projects: readonly { ownerId?: string }[]): string[] => [
  ...new Set(projects.map((project) => project.ownerId).filter((id): id is string => !!id)),
];

export interface BulkResult<T> {
  done: number;
  failed: { item: T; message: string }[];
  // Items the action accepted but only partly carried out (an account suspended whose apps did not all stop).
  partial: { item: T; message: string }[];
}

// One at a time, so each action is audited and answered on its own and a
// failure on one item never stops the others.
export async function runBulk<T, A = unknown>(
  items: readonly T[],
  act: (item: T) => Promise<A>,
  partialOf?: (answer: A) => string | undefined,
): Promise<BulkResult<T>> {
  const result: BulkResult<T> = { done: 0, failed: [], partial: [] };
  const attempt = async (item: T) => {
    try {
      const answer = await act(item);
      result.done += 1;
      const partial = partialOf?.(answer);
      if (partial) result.partial.push({ item, message: partial });
    } catch (error) {
      result.failed.push({ item, message: serverErrorMessage(error, 'The request failed') });
    }
  };
  await items.reduce((previous, item) => previous.then(() => attempt(item)), Promise.resolve());
  return result;
}

// Words for the rows a bulk action leaves out, shown in its dialog.
export function skippedNote(skipped: readonly { name: string; why: string }[]): string {
  if (skipped.length === 0) return '';
  const rows = skipped.map((row) => `${row.name} (${row.why})`);
  return ` Left out: ${rows.join(', ')}.`;
}

// What a suspension could not stop, in words, or nothing when every app stopped.
export function appsFailedNote(answer: unknown): string | undefined {
  const failed = (answer as { appsFailed?: { appName: string; reason: string }[] } | undefined)?.appsFailed ?? [];
  if (failed.length === 0) return undefined;
  const apps = failed.map((app) => `${app.appName} (${app.reason})`);
  return `apps not stopped: ${apps.join(', ')}`;
}

// After a bulk action every admin table, the capacity card and the project lists are read again.
export function refreshAdmin(client: QueryClient): void {
  client.invalidateQueries({ queryKey: ['admin'] });
  client.invalidateQueries({ queryKey: ['instances'] });
  client.invalidateQueries({ queryKey: ['orgs'] });
}
