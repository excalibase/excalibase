import { useState } from 'react';
import { Loader2 } from 'lucide-react';
import type { App, AppSubmission } from '../../api/apps';
import type { TierType } from '../../types';
import { EnvVarEditor, inputClass } from './EnvVarEditor';
import { TIER_ORDER, describeTier, suggestAppName } from './appCopy';
import {
  hasErrors,
  initialValues,
  toAppSubmission,
  validateAppForm,
  type AppFormErrors,
  type AppFormValues,
} from './appFormModel';
import { cn } from '../../utils/cn';

interface AppFormProps {
  readonly tier?: TierType;
  readonly databaseName?: string;
  readonly initial?: App;
  readonly submitLabel: string;
  readonly submitting: boolean;
  readonly serverError?: string;
  readonly onSubmit: (submission: AppSubmission) => void;
  readonly onCancel: () => void;
}

interface FieldProps {
  readonly label: string;
  readonly hint?: string;
  readonly error?: string;
  readonly htmlFor?: string;
  readonly children: React.ReactNode;
}

function Field({ label, hint, error, htmlFor, children }: FieldProps) {
  return (
    <div className="space-y-1.5">
      <label htmlFor={htmlFor} className="block text-sm font-medium text-text-primary">
        {label}
      </label>
      {children}
      {hint && !error && <p className="text-xs text-text-tertiary">{hint}</p>}
      {error && <p className="text-xs text-red-400">{error}</p>}
    </div>
  );
}

function SizePicker({ tier }: { readonly tier?: TierType }) {
  return (
    <div className="grid gap-2 sm:grid-cols-3" data-testid="app-size-picker">
      {TIER_ORDER.map((option) => {
        const selected = option === tier;
        const size = describeTier(option);
        return (
          <div
            key={option}
            aria-current={selected || undefined}
            className={cn(
              'rounded-lg border px-3 py-2',
              selected ? 'border-purple-500 bg-purple-500/10' : 'border-border-primary opacity-50',
            )}
            data-testid={selected ? 'app-size' : `app-size-${option}`}
          >
            <p className="text-sm font-medium text-text-primary">{size.label}</p>
            <p className="text-xs text-text-secondary">{size.detail}</p>
          </div>
        );
      })}
    </div>
  );
}

interface DiskFieldsProps {
  readonly values: AppFormValues;
  readonly errors: AppFormErrors;
  readonly onChange: (patch: Partial<AppFormValues>) => void;
}

function DiskFields({ values, errors, onChange }: DiskFieldsProps) {
  return (
    <div className="bg-surface-card border border-border-primary rounded-lg p-5 space-y-4">
      <label className="flex items-center gap-2 text-sm font-medium text-text-primary">
        <input
          type="checkbox"
          checked={values.diskEnabled}
          disabled={values.diskAttached}
          onChange={(e) => onChange({ diskEnabled: e.target.checked })}
          className="h-4 w-4 accent-purple-500"
          data-testid="app-disk-enabled"
        />
        Persistent disk
      </label>
      <p className="text-xs text-text-tertiary" data-testid="app-disk-note">
        Files written to the disk survive restarts, redeploys and pauses. A container with a disk
        runs one copy, and each deploy stops the old copy before starting the new one, so the
        container is briefly unavailable (usually under a minute). Deleting the container deletes
        the disk. The size is capped by the project's plan and is changed later from the container
        page.
      </p>
      {values.diskEnabled && (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Mount path" htmlFor="app-disk-mount" error={errors.diskMountPath}>
            <input
              id="app-disk-mount"
              value={values.diskMountPath}
              onChange={(e) => onChange({ diskMountPath: e.target.value })}
              className={cn(inputClass, 'font-mono')}
              data-testid="app-disk-mount"
            />
          </Field>
          <Field
            label={values.diskAttached ? 'Size' : 'Size (GiB)'}
            htmlFor="app-disk-size"
            error={errors.diskSize}
            hint={values.diskAttached ? 'Resize the disk from the container page.' : undefined}
          >
            <input
              id="app-disk-size"
              type={values.diskAttached ? 'text' : 'number'}
              min={1}
              value={values.diskSize}
              disabled={values.diskAttached}
              onChange={(e) => onChange({ diskSize: e.target.value })}
              className={inputClass}
              data-testid="app-disk-size"
            />
          </Field>
        </div>
      )}
    </div>
  );
}

// ExposureChoice picks a public web app (a URL through the edge) or an internal
// service reached only by this project's apps (EXC-525).
function ExposureChoice({
  internal,
  onChange,
}: {
  readonly internal: boolean;
  readonly onChange: (internal: boolean) => void;
}) {
  const option = (value: boolean, label: string, hint: string, testId: string) => (
    <label className="flex items-start gap-2 text-sm text-text-primary">
      <input
        type="radio"
        name="app-exposure"
        checked={internal === value}
        onChange={() => onChange(value)}
        data-testid={testId}
        className="mt-1"
      />
      <span>
        {label}
        <span className="block text-xs text-text-tertiary">{hint}</span>
      </span>
    </label>
  );
  return (
    <fieldset className="space-y-2">
      <legend className="text-sm font-medium text-text-primary mb-1">Kind</legend>
      {option(
        false,
        'Public web app',
        'Gets a URL; serves HTTP through the edge.',
        'app-exposure-public',
      )}
      {option(
        true,
        'Internal service',
        "No URL. Reached only by this project's apps on its internal ports (e.g. Redis 6379), with the private network on.",
        'app-exposure-internal',
      )}
    </fieldset>
  );
}

export function AppForm({
  tier,
  databaseName,
  initial,
  submitLabel,
  submitting,
  serverError,
  onSubmit,
  onCancel,
}: AppFormProps) {
  const [values, setValues] = useState<AppFormValues>(() => initialValues(initial));
  const [nameTouched, setNameTouched] = useState(initial !== undefined);
  const [errors, setErrors] = useState<AppFormErrors>({});
  const planReplicas = tier ? describeTier(tier).maxReplicas : 1;
  const maxReplicas = values.diskEnabled ? Math.min(planReplicas, 1) : planReplicas;

  const set = (patch: Partial<AppFormValues>) => setValues((current) => ({ ...current, ...patch }));

  const onImageChange = (image: string) =>
    set(nameTouched ? { image } : { image, name: suggestAppName(image) });

  const handleSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    const found = validateAppForm(values, maxReplicas, databaseName);
    setErrors(found);
    if (!hasErrors(found)) onSubmit(toAppSubmission(values, databaseName));
  };

  return (
    <form onSubmit={handleSubmit} noValidate className="space-y-6 max-w-3xl" data-testid="app-form">
      <div className="bg-surface-card border border-border-primary rounded-lg p-5 space-y-4">
        <Field
          label="Image"
          htmlFor="app-image"
          error={errors.image}
          hint="A public registry image with a tag or digest, for example ghcr.io/acme/web:1.4.0."
        >
          <input
            id="app-image"
            value={values.image}
            onChange={(e) => onImageChange(e.target.value)}
            placeholder="nginx:1.27"
            className={cn(inputClass, 'font-mono')}
            data-testid="app-image"
          />
        </Field>
        <Field
          label="Name"
          htmlFor="app-name"
          error={errors.name}
          hint="Lowercase letters, digits and hyphens."
        >
          <input
            id="app-name"
            value={values.name}
            onChange={(e) => {
              setNameTouched(true);
              set({ name: e.target.value });
            }}
            className={inputClass}
            data-testid="app-name"
          />
        </Field>
        <ExposureChoice internal={values.internal} onChange={(internal) => set({ internal })} />
        <div className="grid gap-4 sm:grid-cols-2">
          {!values.internal && (
            <Field
              label="Port"
              htmlFor="app-port"
              error={errors.port}
              hint="The port your app listens on for HTTP."
            >
              <input
                id="app-port"
                type="number"
                min={1}
                max={65535}
                value={values.port}
                onChange={(e) => set({ port: e.target.value })}
                className={inputClass}
                data-testid="app-port"
              />
            </Field>
          )}
          <Field
            label="Internal TCP ports"
            htmlFor="app-internal-ports"
            error={errors.internalPorts}
            hint={
              values.internal
                ? "Required, e.g. 6379. Reachable only by this project's apps, with its private network on; never from the internet."
                : "Optional, e.g. 6379. Reachable only by this project's apps, with its private network on; never from the internet."
            }
          >
            <input
              id="app-internal-ports"
              value={values.internalPorts}
              onChange={(e) => set({ internalPorts: e.target.value })}
              placeholder="6379, 9092"
              className={inputClass}
              data-testid="app-internal-ports"
            />
          </Field>
          <Field
            label="Copies"
            htmlFor="app-replicas"
            error={errors.replicas}
            hint="0 keeps the container stopped."
          >
            <select
              id="app-replicas"
              value={values.replicas}
              onChange={(e) => set({ replicas: e.target.value })}
              className={inputClass}
              data-testid="app-replicas"
            >
              {Array.from({ length: maxReplicas + 1 }, (_, n) => (
                <option key={n} value={String(n)}>
                  {n}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <Field label="Size" hint="Set by the project's plan. Change the plan to change the size.">
          <SizePicker tier={tier} />
        </Field>
        {!values.internal && (
          <Field
            label="Health check path (optional)"
            htmlFor="app-health"
            error={errors.healthCheckPath}
            hint="A path that answers when the app is ready, for example /healthz."
          >
            <input
              id="app-health"
              value={values.healthCheckPath}
              onChange={(e) => set({ healthCheckPath: e.target.value })}
              placeholder="/healthz"
              className={cn(inputClass, 'font-mono')}
              data-testid="app-health"
            />
          </Field>
        )}
      </div>

      <DiskFields
        values={values}
        errors={errors}
        onChange={(patch) =>
          set(
            patch.diskEnabled && Number(values.replicas) > 1 ? { ...patch, replicas: '1' } : patch,
          )
        }
      />

      <div className="bg-surface-card border border-border-primary rounded-lg p-5 space-y-3">
        <div>
          <h4 className="text-sm font-semibold text-text-primary">Variables</h4>
          <p className="text-xs text-text-tertiary">
            A value, a connection to this project's database, or a secret kept in the project's
            vault.
          </p>
        </div>
        <EnvVarEditor
          rows={values.env}
          errors={errors.env}
          databaseName={databaseName}
          onChange={(env) => set({ env })}
        />
      </div>

      {serverError && (
        <div
          role="alert"
          className="rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400"
        >
          {serverError}
        </div>
      )}

      <div className="flex items-center gap-2">
        <button
          type="submit"
          disabled={submitting}
          className="inline-flex items-center gap-2 px-4 py-2 text-sm rounded-lg bg-purple-500 hover:bg-purple-600 text-white disabled:opacity-50 transition-colors"
          data-testid="app-submit"
        >
          {submitting && <Loader2 className="w-4 h-4 animate-spin" />}
          {submitLabel}
        </button>
        <button
          type="button"
          onClick={onCancel}
          className="px-4 py-2 text-sm rounded-lg text-text-secondary hover:text-text-primary hover:bg-surface-hover transition-colors"
        >
          Cancel
        </button>
      </div>
    </form>
  );
}
