import type { DatabaseInstance } from '../types';

export const DELETION_PROTECTED_REASON =
  'Deletion protection is on. An org owner must turn it off in project Settings before the project can be deleted.';

// The server is the authority: only an explicit true blocks deletion.
export function isDeletionProtected(instance: Pick<DatabaseInstance, 'deletionProtection'> | undefined): boolean {
  return instance?.deletionProtection === true;
}
