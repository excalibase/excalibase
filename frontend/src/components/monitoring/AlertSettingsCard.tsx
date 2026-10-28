import { useState } from 'react';
import { Loader2 } from 'lucide-react';
import { Button } from '../Button';
import { cn } from '../../utils/cn';
import { serverErrorMessage } from '../../utils/serverError';
import {
  useAlertSettings,
  useSaveAlertSettings,
  useSendTestAlert,
} from '../../hooks/useMonitoring';
import type { AlertSettings, AlertTestResult } from '../../api/monitoring';
import {
  bodyFromForm,
  formFromSettings,
  MAX_ALERT_EMAILS,
  THRESHOLD_FIELDS,
  type AlertSettingsForm,
} from '../../utils/alertSettingsForm';

const CHANNEL_LABELS: Record<AlertTestResult['channel'], string> = {
  email: 'Email',
  webhook: 'Webhook',
};

const inputClass =
  'w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-sm text-text-primary disabled:opacity-60';

function resultLine(result: AlertTestResult): string {
  const outcome = result.ok ? 'sent' : `failed, ${result.error ?? 'no reason given'}`;
  return `${CHANNEL_LABELS[result.channel]}: ${outcome}`;
}

function NumberField({
  id,
  label,
  value,
  disabled,
  placeholder,
  onChange,
}: {
  readonly id: string;
  readonly label: string;
  readonly value: string;
  readonly disabled: boolean;
  readonly placeholder?: string;
  readonly onChange: (value: string) => void;
}) {
  return (
    <div>
      <label htmlFor={id} className="block text-xs text-text-secondary mb-1">
        {label}
      </label>
      <input
        id={id}
        type="text"
        inputMode="decimal"
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
        className={inputClass}
      />
    </div>
  );
}

function webhookState(settings: AlertSettings, clearing: boolean): string {
  if (!settings.webhookSet) return 'No webhook set';
  if (clearing) return 'Removed on save';
  return `${settings.webhookUrl ?? 'Webhook'} (set)`;
}

function AlertSettingsEditor({
  settings,
  canMutate,
}: {
  readonly settings: AlertSettings;
  readonly canMutate: boolean;
}) {
  const [form, setForm] = useState<AlertSettingsForm>(() => formFromSettings(settings));
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [results, setResults] = useState<AlertTestResult[] | null>(null);
  const save = useSaveAlertSettings();
  const sendTest = useSendTestAlert();
  const disabled = !canMutate;

  const update = <K extends keyof AlertSettingsForm>(key: K, value: AlertSettingsForm[K]) =>
    setForm((current) => ({ ...current, [key]: value }));

  const handleSave = async () => {
    setError(null);
    setNotice(null);
    const { body, error: invalid } = bodyFromForm(form);
    if (!body) {
      setError(invalid ?? 'The settings could not be read.');
      return;
    }
    try {
      const saved = await save.mutateAsync(body);
      setForm(formFromSettings(saved));
      setNotice('Saved.');
    } catch (err) {
      setError(serverErrorMessage(err, 'The settings could not be saved'));
    }
  };

  const handleTest = async () => {
    setError(null);
    setResults(null);
    try {
      setResults((await sendTest.mutateAsync()).results);
    } catch (err) {
      setError(serverErrorMessage(err, 'The test could not be sent'));
    }
  };

  return (
    <div className="space-y-5">
      <div className="flex items-center gap-3">
        <button
          type="button"
          role="switch"
          aria-checked={form.enabled}
          aria-label="Alerts enabled"
          disabled={disabled}
          onClick={() => update('enabled', !form.enabled)}
          className={cn(
            'relative inline-flex h-5 w-9 items-center rounded-full transition-colors disabled:opacity-60',
            form.enabled ? 'bg-accent-primary' : 'bg-bg-tertiary',
          )}
        >
          <span
            className={cn(
              'inline-block h-4 w-4 rounded-full bg-white transition-transform',
              form.enabled ? 'translate-x-4' : 'translate-x-0.5',
            )}
          />
        </button>
        <span className="text-sm text-text-primary">
          {form.enabled ? 'Alerts on' : 'Alerts off'}
        </span>
      </div>

      <div>
        <p className="text-xs text-text-tertiary mb-2">Leave a threshold empty to turn it off.</p>
        <div className="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6 gap-3">
          {THRESHOLD_FIELDS.map(({ key, label }) => (
            <NumberField
              key={key}
              id={`alert-${key}`}
              label={label}
              value={form[key]}
              disabled={disabled}
              placeholder="off"
              onChange={(value) => update(key, value)}
            />
          ))}
        </div>
      </div>

      <div className="grid grid-cols-2 gap-3 max-w-md">
        <NumberField
          id="alert-window"
          label="Window minutes"
          value={form.windowMinutes}
          disabled={disabled}
          onChange={(value) => update('windowMinutes', value)}
        />
        <NumberField
          id="alert-down"
          label="Down minutes"
          value={form.downMinutes}
          disabled={disabled}
          onChange={(value) => update('downMinutes', value)}
        />
      </div>

      <div>
        <label htmlFor="alert-emails" className="block text-xs text-text-secondary mb-1">
          Email recipients
        </label>
        <textarea
          id="alert-emails"
          rows={3}
          value={form.emails}
          disabled={disabled}
          onChange={(event) => update('emails', event.target.value)}
          className={inputClass}
        />
        <p className="text-xs text-text-tertiary mt-1">
          Comma or line separated, at most {MAX_ALERT_EMAILS}.
        </p>
      </div>

      <div className="space-y-2">
        <p className="text-xs text-text-secondary">Webhook</p>
        <div className="flex items-center gap-3 flex-wrap">
          <span
            className="text-sm text-text-primary font-mono break-all"
            data-testid="webhook-state"
          >
            {webhookState(settings, form.clearWebhook)}
          </span>
          {canMutate && settings.webhookSet && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => update('clearWebhook', !form.clearWebhook)}
            >
              {form.clearWebhook ? 'Keep webhook' : 'Clear webhook'}
            </Button>
          )}
        </div>
        {!form.clearWebhook && (
          <div className="max-w-xl">
            <label htmlFor="alert-webhook" className="block text-xs text-text-secondary mb-1">
              New webhook URL
            </label>
            <input
              id="alert-webhook"
              type="url"
              value={form.webhookInput}
              disabled={disabled}
              placeholder="https://"
              autoComplete="off"
              onChange={(event) => update('webhookInput', event.target.value)}
              className={inputClass}
            />
          </div>
        )}
      </div>

      {error && (
        <p role="alert" className="text-sm text-red-400">
          {error}
        </p>
      )}
      {notice && <p className="text-sm text-green-400">{notice}</p>}
      {results && (
        <ul className="text-sm space-y-1" data-testid="test-results">
          {results.map((result) => (
            <li key={result.channel} className={result.ok ? 'text-green-400' : 'text-red-400'}>
              {resultLine(result)}
            </li>
          ))}
        </ul>
      )}

      <div className="flex gap-2">
        <Button onClick={() => void handleSave()} disabled={disabled || save.isPending}>
          Save
        </Button>
        <Button
          variant="secondary"
          onClick={() => void handleTest()}
          disabled={disabled || sendTest.isPending}
        >
          Send test
        </Button>
      </div>
    </div>
  );
}

export function AlertSettingsCard({ canMutate }: { readonly canMutate: boolean }) {
  const { data, isLoading, isError } = useAlertSettings();
  let body: React.ReactNode;
  if (isLoading) {
    body = <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" />;
  } else if (isError || !data) {
    body = <p className="text-sm text-text-secondary">Alert settings could not be loaded.</p>;
  } else {
    body = <AlertSettingsEditor settings={data} canMutate={canMutate} />;
  }
  return (
    <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4">
      <div>
        <h2 className="text-lg font-semibold">Alert settings</h2>
        <p className="text-xs text-text-tertiary mt-0.5">
          Alerts fire when a threshold holds for the whole window, or a container is down for the
          down minutes.
        </p>
      </div>
      {body}
    </div>
  );
}
