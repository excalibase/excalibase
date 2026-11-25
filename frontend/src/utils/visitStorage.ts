// Where the Recently visited list is kept: one list per person, so a shared
// browser never shows one person's pages to the next. Shared by the store and
// the API client, which both have to forget it when a session ends.
const PREFIX = 'studio.recentlyVisited';

export const visitsKey = (userId: string): string => `${PREFIX}.${userId}`;

function storedKeys(): string[] {
  const keys: string[] = [];
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i);
    if (key?.startsWith(PREFIX)) keys.push(key);
  }
  return keys;
}

// Drops every list but the one named (or all of them): a list left by an earlier
// person, or by the version that kept a single shared list.
export function pruneVisits(keep?: string): void {
  try {
    for (const key of storedKeys()) if (key !== keep) localStorage.removeItem(key);
  } catch {
    // Browser storage is blocked: there is nothing kept to drop.
  }
}

export const clearVisits = (): void => pruneVisits();
