import { useState } from 'react';
import {
  DATADOG_SITES,
  httpsUrlProblem,
  type CreateLogDrain,
  type LogDrain,
  type LogDrainKind,
  type UpdateLogDrain,
} from '../api/logDrains';
import { primaryButton, secondaryButton } from './containers/ContainerBits';

export interface DrainValues {
  readonly name: string;
  readonly kind: LogDrainKind;
  readonly datadogSite: string;
  readonly url: string;
  readonly headerName: string;
  readonly secret: string;
}

const inputClass =
  'w-full min-w-0 px-3 py-1.5 text-sm rounded-lg bg-bg-tertiary border border-border-primary text-text-primary focus:outline-none focus:border-purple-500/50';

export const emptyDrainValues: DrainValues = {
  name: '',
  kind: 'datadog',
  datadogSite: 'us1',
  url: '',
  headerName: '',
  secret: '',
};

// The URL starts empty: the server returns only a masked destination, never the URL itself.
export function valuesOf(drain: LogDrain): DrainValues {
  return {
    name: drain.name,
    kind: drain.kind,
    datadogSite: drain.datadogSite ?? 'us1',
    url: '',
    headerName: drain.headerName ?? '',
    secret: '',
  };
}

export const REENTER_HEADER_VALUE = 'Re-enter the header value to send it to a new host or header';

function hostOf(url: string): string | undefined {
  try {
    return new URL(url.trim()).host.toLowerCase();
  } catch {
    return undefined;
  }
}

const hasStoredHeader = (current?: LogDrain) => !!current?.headerName && current.hasSecret;

// A stored header value never follows the drain to a new host or header name.
export function needsHeaderValueAgain(values: DrainValues, current?: LogDrain): boolean {
  if (!current || values.kind !== 'https' || !hasStoredHeader(current)) return false;
  if (values.secret.trim() !== '') return false;
  const header = values.headerName.trim();
  if (header === '') return false;
  const newHost = values.url.trim() !== '' && hostOf(values.url) !== hostOf(current.url ?? '');
  return newHost || header !== current.headerName;
}

// The contract's rules, checked before anything is sent; current is the drain being edited.
export function drainProblem(values: DrainValues, current?: LogDrain): string | undefined {
  if (!values.name.trim()) return 'Give the drain a name';
  const secret = values.secret.trim();
  if (values.kind === 'datadog') {
    return secret || current?.hasSecret ? undefined : 'A Datadog API key is required';
  }
  if (!current || values.url.trim() !== '') {
    const urlProblem = httpsUrlProblem(values.url);
    if (urlProblem) return urlProblem;
  }
  if (needsHeaderValueAgain(values, current)) return REENTER_HEADER_VALUE;
  const header = values.headerName.trim();
  const headerWithoutValue = header !== '' && secret === '' && !hasStoredHeader(current);
  const valueWithoutHeader = header === '' && secret !== '';
  return headerWithoutValue || valueWithoutHeader
    ? 'Give both a header name and a value, or neither'
    : undefined;
}

export function createBody(values: DrainValues): CreateLogDrain {
  const base = { name: values.name.trim(), kind: values.kind };
  if (values.kind === 'datadog') {
    return { ...base, datadogSite: values.datadogSite, secret: values.secret.trim() };
  }
  const header = values.headerName.trim();
  return header
    ? { ...base, url: values.url.trim(), headerName: header, secret: values.secret.trim() }
    : { ...base, url: values.url.trim() };
}

// Only what changed is sent: a blank URL or secret keeps the stored one, and a
// cleared header never carries a secret.
export function updateBody(values: DrainValues, current: LogDrain): UpdateLogDrain {
  const name = values.name.trim();
  const secret = values.secret.trim();
  if (values.kind === 'datadog') {
    return { name, datadogSite: values.datadogSite, ...(secret ? { secret } : {}) };
  }
  const url = values.url.trim();
  const header = values.headerName.trim();
  return {
    name,
    ...(url ? { url } : {}),
    ...(header === (current.headerName ?? '') ? {} : { headerName: header }),
    ...(secret && header ? { secret } : {}),
  };
}

interface DrainFormProps {
  readonly initial: DrainValues;
  // The drain being edited; absent when adding one.
  readonly current?: LogDrain;
  readonly submitLabel: string;
  readonly disabled?: boolean;
  readonly pending: boolean;
  readonly serverError?: string;
  readonly onSubmit: (values: DrainValues, clear: () => void) => void;
  readonly onCancel?: () => void;
}

function SecretField({
  values,
  hasSecret,
  onChange,
}: {
  readonly values: DrainValues;
  readonly hasSecret: boolean;
  readonly onChange: (secret: string) => void;
}) {
  const label = values.kind === 'datadog' ? 'Datadog API key' : 'Header value';
  let placeholder = values.kind === 'datadog' ? 'API key' : 'Header value, such as Bearer …';
  if (hasSecret) placeholder = '•••• stored, leave blank to keep';
  return (
    <input
      type="password"
      autoComplete="off"
      value={values.secret}
      onChange={(event) => onChange(event.target.value)}
      placeholder={placeholder}
      aria-label={label}
      className={inputClass}
      data-testid="log-drain-secret"
    />
  );
}

function DestinationFields({
  values,
  current,
  set,
}: {
  readonly values: DrainValues;
  readonly current?: LogDrain;
  readonly set: (patch: Partial<DrainValues>) => void;
}) {
  if (values.kind === 'datadog') {
    return (
      <select
        value={values.datadogSite}
        onChange={(event) => set({ datadogSite: event.target.value })}
        aria-label="Datadog site"
        className={inputClass}
        data-testid="log-drain-site"
      >
        {DATADOG_SITES.map((site) => (
          <option key={site.value} value={site.value}>
            {site.label} ({site.host})
          </option>
        ))}
      </select>
    );
  }
  return (
    <>
      <input
        type="url"
        value={values.url}
        onChange={(event) => set({ url: event.target.value })}
        placeholder={
          current?.url ? `${current.url} (leave blank to keep)` : 'https://logs.example.com/ingest'
        }
        aria-label="Endpoint URL"
        className={inputClass}
        data-testid="log-drain-url"
      />
      <input
        type="text"
        value={values.headerName}
        onChange={(event) => set({ headerName: event.target.value })}
        placeholder="Header name (optional), such as Authorization"
        aria-label="Header name"
        className={inputClass}
        data-testid="log-drain-header"
      />
    </>
  );
}

export function DrainForm({
  initial,
  current,
  submitLabel,
  disabled = false,
  pending,
  serverError,
  onSubmit,
  onCancel,
}: DrainFormProps) {
  const [values, setValues] = useState<DrainValues>(initial);
  const [problem, setProblem] = useState<string>();
  const set = (patch: Partial<DrainValues>) => setValues((state) => ({ ...state, ...patch }));

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    const found = drainProblem(values, current);
    setProblem(found);
    if (found) return;
    onSubmit(values, () => setValues((state) => ({ ...(current ? state : initial), secret: '' })));
  };

  const message = problem ?? serverError;
  const reenter = needsHeaderValueAgain(values, current);
  return (
    <form onSubmit={submit} className="space-y-2" data-testid="log-drain-form">
      <div className="grid gap-2 sm:grid-cols-2">
        <input
          type="text"
          value={values.name}
          maxLength={64}
          onChange={(event) => set({ name: event.target.value })}
          placeholder="Drain name, such as datadog-prod"
          aria-label="Drain name"
          className={inputClass}
          data-testid="log-drain-name"
        />
        {!current && (
          <select
            value={values.kind}
            onChange={(event) => set({ kind: event.target.value as LogDrainKind, secret: '' })}
            aria-label="Destination"
            className={inputClass}
            data-testid="log-drain-kind"
          >
            <option value="datadog">Datadog</option>
            <option value="https">HTTPS endpoint</option>
          </select>
        )}
        <DestinationFields values={values} current={current} set={set} />
        <SecretField
          values={values}
          hasSecret={!!current?.hasSecret && !reenter}
          onChange={(secret) => set({ secret })}
        />
      </div>
      {reenter && (
        <p className="text-xs text-amber-400" data-testid="log-drain-reenter">
          {REENTER_HEADER_VALUE}
        </p>
      )}
      {message && (
        <p role="alert" className="text-xs text-red-400">
          {message}
        </p>
      )}
      <div className="flex gap-2">
        <button
          type="submit"
          disabled={disabled || pending || reenter}
          className={primaryButton}
          data-testid="log-drain-submit"
        >
          {submitLabel}
        </button>
        {onCancel && (
          <button type="button" onClick={onCancel} className={secondaryButton}>
            Cancel
          </button>
        )}
      </div>
    </form>
  );
}
