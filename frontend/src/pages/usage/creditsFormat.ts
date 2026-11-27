const MICROS_PER_DOLLAR = 1_000_000;
const DAY_MS = 86_400_000;
const AMOUNT = /^(\d{1,7})(?:\.(\d{1,6}))?$/;

// The text without its trailing zeros, in one pass, whatever its length.
export function trimTrailingZeros(text: string): string {
  let end = text.length;
  while (end > 0 && text[end - 1] === '0') end -= 1;
  return text.slice(0, end);
}

// Dollars with at least two decimals and no trailing zeros beyond them, from whole micros.
export function dollars(micros: number): string {
  const sign = micros < 0 ? '-' : '';
  const abs = Math.abs(micros);
  const whole = Math.floor(abs / MICROS_PER_DOLLAR);
  const fraction = trimTrailingZeros(String(abs % MICROS_PER_DOLLAR).padStart(6, '0')).padEnd(2, '0');
  return `${sign}$${whole.toLocaleString('en-US')}.${fraction}`;
}

// "12.5" is 12,500,000 micros; anything that is not a plain positive amount is null.
export function microsFromDollars(text: string): number | null {
  const match = AMOUNT.exec(text.trim());
  if (!match) return null;
  const micros = Number(match[1]) * MICROS_PER_DOLLAR + Number((match[2] ?? '').padEnd(6, '0'));
  return micros > 0 ? micros : null;
}

// Whole days until an instant, rounded up; never negative.
export function daysLeft(expiresAt: string | null, now: Date): number | null {
  if (!expiresAt) return null;
  const at = Date.parse(expiresAt);
  if (Number.isNaN(at)) return null;
  return Math.max(0, Math.ceil((at - now.getTime()) / DAY_MS));
}

// The UTC month a statement is for, as YYYY-MM.
export function utcMonth(now: Date): string {
  return now.toISOString().slice(0, 7);
}

// The last few UTC months, newest first, for the statement picker.
export function recentMonths(now: Date, count: number): string[] {
  const months: string[] = [];
  for (let back = 0; back < count; back += 1) {
    months.push(utcMonth(new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - back, 1))));
  }
  return months;
}
