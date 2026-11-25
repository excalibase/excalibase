// The platform writes every time as RFC 3339 with a zone. A string without one
// is read by the browser as local time, so it is off by the viewer's offset
// and nobody notices; refuse it instead and show nothing.
const ZONED = /(?:Z|[+-]\d{2}:?\d{2})$/i;

export function parseInstant(value: string | number | null | undefined): number {
  if (typeof value === 'number') return value;
  if (!value) return Number.NaN;
  if (!ZONED.test(value)) {
    console.warn(`Ignoring a time with no zone: ${value}`);
    return Number.NaN;
  }
  // Date reads three fraction digits; the server sends up to nine.
  return Date.parse(value.replace(/(\.\d{3})\d+/, '$1'));
}
