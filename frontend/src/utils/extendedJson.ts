const int32Min = -2147483648;
const int32Max = 2147483647;
const isoDateMax = Date.UTC(9999, 11, 31, 23, 59, 59, 999);

function isInt32(n: number): boolean {
  return Number.isInteger(n) && n >= int32Min && n <= int32Max;
}

type Wrapper = Record<string, unknown>;

function simplifyNumber(key: string, raw: string, original: Wrapper): unknown {
  const n = Number(raw);
  if (key === '$numberInt') return n;
  if (key === '$numberDouble') return Number.isFinite(n) && !Number.isInteger(n) ? n : original;
  return Number.isSafeInteger(n) && !isInt32(n) ? n : original;
}

function simplifyDate(value: unknown, original: Wrapper): unknown {
  const millis = Number((value as Wrapper | null)?.$numberLong);
  return Number.isFinite(millis) && millis >= 0 && millis <= isoDateMax ? { $date: new Date(millis).toISOString() } : original;
}

function simplifyWrapper(value: Wrapper): unknown {
  const [key] = Object.keys(value);
  const inner = value[key];
  if ((key === '$numberInt' || key === '$numberDouble' || key === '$numberLong') && typeof inner === 'string') {
    return simplifyNumber(key, inner, value);
  }
  if (key === '$date') return simplifyDate(inner, value);
  return value;
}

// Canonical Extended JSON written as plainly as it can be without changing a
// type when it is read back: a plain whole number reads as an int32 (or an
// int64 beyond that range) and a fractional one as a double, so only the
// values that would read back differently keep their wrapper.
export function toEditable(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(toEditable);
  if (typeof value !== 'object' || value === null) return value;
  const keys = Object.keys(value);
  if (keys.length > 0 && keys.every((key) => key.startsWith('$'))) {
    return keys.length === 1 ? simplifyWrapper(value as Wrapper) : value;
  }
  return Object.fromEntries(Object.entries(value).map(([key, child]) => [key, toEditable(child)]));
}

export function formatDocument(value: unknown): string {
  return JSON.stringify(toEditable(value), null, 2);
}
