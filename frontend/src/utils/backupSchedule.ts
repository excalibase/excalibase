const DAY_NAMES = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
const FALLBACK = 'Scheduled backups';
const NIGHT_HOURS = new Set([0, 1, 2, 3, 4, 5]);

function pad(value: number): string {
  return String(value).padStart(2, '0');
}

function timeOf(minute: number, hour: number): string {
  return `${pad(hour)}:${pad(minute)} UTC`;
}

// Plain words for the common daily, hourly and weekly cron shapes; anything
// else is "Scheduled backups" so a customer never reads a raw cron.
export function describeBackupSchedule(cron: string | undefined): string {
  const fields = (cron ?? '').trim().split(/\s+/);
  if (fields.length !== 5) return FALLBACK;
  const [minuteField, hourField, dayOfMonth, month, weekday] = fields;
  if (dayOfMonth !== '*' || month !== '*' || !/^\d{1,2}$/.test(minuteField)) return FALLBACK;
  const minute = Number(minuteField);
  if (minute > 59) return FALLBACK;
  if (hourField === '*' && weekday === '*') {
    return minute === 0 ? 'Hourly backups' : `Hourly backups at ${minute} minutes past`;
  }
  if (!/^\d{1,2}$/.test(hourField) || Number(hourField) > 23) return FALLBACK;
  const hour = Number(hourField);
  if (weekday === '*') {
    const word = NIGHT_HOURS.has(hour) && minute === 0 ? 'Nightly' : 'Daily';
    return `${word} backups at ${timeOf(minute, hour)}`;
  }
  if (/^[0-7]$/.test(weekday)) {
    return `Weekly backups on ${DAY_NAMES[Number(weekday) % 7]} at ${timeOf(minute, hour)}`;
  }
  return FALLBACK;
}
