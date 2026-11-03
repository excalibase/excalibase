import type { AlertSettings, AlertSettingsUpdate, AlertThresholds } from '../api/monitoring';

export const MAX_ALERT_EMAILS = 10;

export interface AlertSettingsForm {
  enabled: boolean;
  cpuPercent: string;
  memoryPercent: string;
  diskPercent: string;
  temperatureCelsius: string;
  load5: string;
  networkMbps: string;
  windowMinutes: string;
  downMinutes: string;
  emails: string;
  webhookInput: string;
  clearWebhook: boolean;
}

export const THRESHOLD_FIELDS: readonly { key: keyof AlertThresholds; label: string }[] = [
  { key: 'cpuPercent', label: 'CPU %' },
  { key: 'memoryPercent', label: 'Memory %' },
  { key: 'diskPercent', label: 'Disk %' },
  { key: 'temperatureCelsius', label: 'Temperature °C' },
  { key: 'load5', label: 'Load 5' },
  { key: 'networkMbps', label: 'Network Mbps' },
];

const text = (value: number | null) => (value == null ? '' : String(value));

export function formFromSettings(settings: AlertSettings): AlertSettingsForm {
  return {
    enabled: settings.enabled,
    cpuPercent: text(settings.cpuPercent),
    memoryPercent: text(settings.memoryPercent),
    diskPercent: text(settings.diskPercent),
    temperatureCelsius: text(settings.temperatureCelsius),
    load5: text(settings.load5),
    networkMbps: text(settings.networkMbps),
    windowMinutes: String(settings.windowMinutes),
    downMinutes: String(settings.downMinutes),
    emails: settings.emails.join('\n'),
    webhookInput: '',
    clearWebhook: false,
  };
}

export function parseEmails(raw: string): string[] {
  return raw
    .split(/[,\n]/)
    .map((email) => email.trim())
    .filter((email) => email !== '');
}

type Parsed = { value: number | null; error?: undefined } | { value?: undefined; error: string };

// Empty means "off" for a threshold; a required field refuses it.
function parseNumber(raw: string, label: string, required: boolean): Parsed {
  const trimmed = raw.trim();
  if (trimmed === '') return required ? { error: `${label} must be a number.` } : { value: null };
  const value = Number(trimmed);
  if (!Number.isFinite(value)) return { error: `${label} must be a number.` };
  if (value < 0) return { error: `${label} must be 0 or more.` };
  return { value };
}

export interface FormResult {
  body?: AlertSettingsUpdate;
  error?: string;
}

export function bodyFromForm(form: AlertSettingsForm): FormResult {
  const thresholds: Partial<AlertThresholds> = {};
  for (const { key, label } of THRESHOLD_FIELDS) {
    const parsed = parseNumber(form[key], label, false);
    if (parsed.error) return { error: parsed.error };
    thresholds[key] = parsed.value ?? null;
  }
  const windowMinutes = parseNumber(form.windowMinutes, 'Window minutes', true);
  if (windowMinutes.error) return { error: windowMinutes.error };
  const downMinutes = parseNumber(form.downMinutes, 'Down minutes', true);
  if (downMinutes.error) return { error: downMinutes.error };
  const emails = parseEmails(form.emails);
  if (emails.length > MAX_ALERT_EMAILS)
    return { error: `At most ${MAX_ALERT_EMAILS} email recipients.` };

  const body: AlertSettingsUpdate = {
    enabled: form.enabled,
    ...(thresholds as AlertThresholds),
    windowMinutes: windowMinutes.value ?? 0,
    downMinutes: downMinutes.value ?? 0,
    emails,
  };
  const webhook = form.webhookInput.trim();
  if (form.clearWebhook) return { body: { ...body, webhookUrl: '' } };
  if (webhook !== '') return { body: { ...body, webhookUrl: webhook } };
  return { body };
}
