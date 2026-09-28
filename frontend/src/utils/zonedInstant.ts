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
