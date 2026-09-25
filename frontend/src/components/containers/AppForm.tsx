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
  const maxReplicas = tier ? describeTier(tier).maxReplicas : 1;

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
        <div className="grid gap-4 sm:grid-cols-2">
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
      </div>

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
