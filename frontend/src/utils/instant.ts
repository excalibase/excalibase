// The platform writes every time as RFC 3339 with a zone. A string without one
// is read by the browser as local time, so it is off by the viewer's offset
// and nobody notices; refuse it instead and show nothing.
const ZONED = /(?:Z|[+-]\d{2}:?\d{2})$/i;
const UNKNOWN = 'unknown';
const warned = new Set<string>();

export function parseInstant(value: string | number | null | undefined): number {
  if (typeof value === 'number') return value;
  if (!value) return Number.NaN;
  if (!ZONED.test(value)) {
    if (!warned.has(value)) {
      warned.add(value);
      console.warn(`Ignoring a time with no zone: ${value}`);
    }
    return Number.NaN;
  }
  return Date.parse(value);
}

type InstantStyle = 'datetime' | 'date' | 'time';

// The one way a time reaches the screen: the viewer's zone, or "unknown" when
// the value cannot be read, never "Invalid Date".
export function formatInstant(value: string | number | null | undefined, style: InstantStyle = 'datetime'): string {
  const at = new Date(parseInstant(value));
  if (Number.isNaN(at.getTime())) return UNKNOWN;
  if (style === 'date') return at.toLocaleDateString();
  return style === 'time' ? at.toLocaleTimeString() : at.toLocaleString();
}
