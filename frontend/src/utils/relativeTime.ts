import { parseInstant } from './instant';

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

// "5 min ago" for the rows of a busy list; the exact time belongs in a title.
export function relativeTime(value: string | null | undefined, now: number = Date.now()): string {
  const at = parseInstant(value);
  if (Number.isNaN(at)) return 'unknown';
  const age = Math.max(0, now - at);
  if (age < MINUTE) return 'just now';
  if (age < HOUR) return `${Math.floor(age / MINUTE)} min ago`;
  if (age < DAY) return `${Math.floor(age / HOUR)} h ago`;
  return `${Math.floor(age / DAY)} d ago`;
}
