import type { AppBuilder, AppBuildSettings } from '../../api/appBuilds';
import { envToText, textToEnv } from '../sites/SiteBuildSettingsForm';

const inputClass =
  'w-full rounded-md border border-border-primary bg-surface-primary px-3 py-1.5 text-sm text-text-primary';

const BUILDER_LABELS: Record<AppBuilder, string> = {
  auto: 'Automatic',
  dockerfile: 'Dockerfile',
  buildpacks: 'Buildpacks',
};

function Field({
  label,
  hint,
  children,
}: {
  readonly label: string;
  readonly hint: string;
  readonly children: React.ReactNode;
}) {
  return (
    <label className="block space-y-1 text-sm text-text-primary">
      <span>{label}</span>
      {children}
      <span className="block text-xs text-text-tertiary">{hint}</span>
    </label>
  );
}

interface Props {
  readonly settings: AppBuildSettings;
  readonly builders: AppBuilder[];
  readonly onChange: (settings: AppBuildSettings) => void;
}

export function AppBuildSettingsForm({ settings, builders, onChange }: Props) {
  const set =
    (field: 'branch' | 'rootDir' | 'dockerfilePath') => (event: { target: { value: string } }) =>
      onChange({ ...settings, [field]: event.target.value });
  return (
    <div className="grid gap-3 sm:grid-cols-2" data-testid="app-build-settings">
      <Field label="Branch" hint="Pushes to this branch deploy. Empty: the default branch.">
        <input
          className={inputClass}
          value={settings.branch}
          onChange={set('branch')}
          data-testid="app-build-branch"
        />
      </Field>
      <Field
        label="Builder"
        hint="Automatic uses your Dockerfile when the repository has one, otherwise Buildpacks detect the language."
      >
        <select
          className={inputClass}
          value={settings.builder}
          onChange={(event) => onChange({ ...settings, builder: event.target.value as AppBuilder })}
          data-testid="app-build-builder"
        >
          {builders.map((builder) => (
            <option key={builder} value={builder}>
              {BUILDER_LABELS[builder] ?? builder}
            </option>
          ))}
        </select>
      </Field>
      <Field
        label="Root directory"
        hint="The build context in a monorepo. Empty: the repository root."
      >
        <input
          className={inputClass}
          value={settings.rootDir}
          onChange={set('rootDir')}
          data-testid="app-build-root"
        />
      </Field>
      <Field label="Dockerfile path" hint="Relative to the root directory. Empty: Dockerfile.">
        <input
          className={inputClass}
          value={settings.dockerfilePath}
          onChange={set('dockerfilePath')}
          data-testid="app-build-dockerfile"
        />
      </Field>
      <div className="sm:col-span-2">
        <Field
          label="Build arguments"
          hint="One NAME=value per line. They end up in the image's history: never put a secret here."
        >
          <textarea
            className={`${inputClass} font-mono h-24`}
            defaultValue={envToText(settings.buildArgs)}
            onBlur={(event) => onChange({ ...settings, buildArgs: textToEnv(event.target.value) })}
            data-testid="app-build-args"
          />
        </Field>
      </div>
    </div>
  );
}
