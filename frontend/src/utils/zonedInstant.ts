// A datetime-local value has no zone; the platform refuses zone-less times,
// so send the instant the browser displayed, in UTC.
export function toZonedInstant(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  if (!trimmed) return undefined;
  const instant = new Date(trimmed);
  if (Number.isNaN(instant.getTime())) {
    throw new Error(`Not a valid time: ${trimmed}`);
  }
  return instant.toISOString();
}

// The datetime-local value that displays an instant in the browser's zone,
// to the second.
export function toLocalInput(instant: string): string {
  const at = new Date(instant);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}`;
}
