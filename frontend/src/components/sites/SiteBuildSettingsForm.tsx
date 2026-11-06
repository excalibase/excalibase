import { useEffect, useState } from 'react';
import type { SiteBuildSettings } from '../../api/siteBuilds';

const inputClass =
  'w-full rounded-md border border-border-primary bg-surface-primary px-3 py-1.5 text-sm text-text-primary';

interface Props {
  readonly settings: SiteBuildSettings;
  readonly nodeVersions: string[];
  readonly onChange: (settings: SiteBuildSettings) => void;
  // Told whether the variables box holds something that cannot be saved as written.
  readonly onProblem?: (problem: string | null) => void;
}

// Variables travel as one KEY=value line each, the way a .env file reads.
export const envToText = (env: Record<string, string>) =>
  Object.entries(env)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, value]) => `${name}=${value}`)
    .join('\n');

export const textToEnv = (text: string): Record<string, string> =>
  Object.fromEntries(
    text
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => line !== '' && !line.startsWith('#') && line.includes('='))
      .map((line) => [line.slice(0, line.indexOf('=')).trim(), line.slice(line.indexOf('=') + 1)]),
  );

// A line the box cannot read as NAME=value; the line is named by number, never quoted, as it may hold a secret.
export const envTextProblem = (text: string): string | null => {
  const lines = text.split('\n');
  const bad = lines.findIndex((line) => {
    const trimmed = line.trim();
    return trimmed !== '' && !trimmed.startsWith('#') && !trimmed.includes('=');
  });
  return bad < 0 ? null : `Line ${bad + 1} is not NAME=value. Nothing is saved until it is fixed.`;
};

// The box holds one line per variable, so a value with a line break set through the API cannot be shown or kept by it.
export const multilineEnvNames = (env: Record<string, string>): string[] =>
  Object.entries(env)
    .filter(([, value]) => value.includes('\n'))
    .map(([name]) => name)
    .sort();

const withoutNames = (env: Record<string, string>, names: string[]) =>
  Object.fromEntries(Object.entries(env).filter(([name]) => !names.includes(name)));

// One NAME=value per line, shared by site build variables and app build arguments.
export function EnvField({
  label,
  hint,
  env,
  testId,
  onChange,
  onProblem,
}: {
  readonly label: string;
  readonly hint: string;
  readonly env: Record<string, string>;
  readonly testId: string;
  readonly onChange: (env: Record<string, string>) => void;
  readonly onProblem?: (problem: string | null) => void;
}) {
  const multiline = multilineEnvNames(env);
  const [typedProblem, setTypedProblem] = useState<string | null>(null);
  const keptProblem =
    multiline.length > 0
      ? `${multiline.join(', ')} ${multiline.length === 1 ? 'has' : 'have'} a value with line breaks, which this box cannot show or keep. Change it through the API, then edit here.`
      : null;
  const problem = typedProblem ?? keptProblem;
  useEffect(() => onProblem?.(problem), [problem, onProblem]);
  return (
    <Field label={label} hint={hint}>
      <textarea
        className={`${inputClass} font-mono h-24`}
        defaultValue={envToText(withoutNames(env, multiline))}
        onBlur={(event) => {
          const found = envTextProblem(event.target.value);
          setTypedProblem(found);
          if (found === null)
            onChange({ ...textToEnv(event.target.value), ...pickNames(env, multiline) });
        }}
        data-testid={testId}
      />
      {problem && (
        <span role="alert" className="block text-xs text-red-400" data-testid={`${testId}-error`}>
          {problem}
        </span>
      )}
    </Field>
  );
}

const pickNames = (env: Record<string, string>, names: string[]) =>
  Object.fromEntries(Object.entries(env).filter(([name]) => names.includes(name)));

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

export function SiteBuildSettingsForm({ settings, nodeVersions, onChange, onProblem }: Props) {
  const set = (field: keyof SiteBuildSettings) => (event: { target: { value: string } }) =>
    onChange({ ...settings, [field]: event.target.value });
  return (
    <div className="grid gap-3 sm:grid-cols-2" data-testid="site-build-settings">
      <Field label="Branch" hint="Pushes to this branch deploy. Empty: the default branch.">
        <input
          className={inputClass}
          value={settings.branch}
          onChange={set('branch')}
          data-testid="build-branch"
        />
      </Field>
      <Field
        label="Root directory"
        hint="The frontend's folder in a monorepo. Empty: the repository root."
      >
        <input
          className={inputClass}
          value={settings.rootDir}
          onChange={set('rootDir')}
          data-testid="build-root"
        />
      </Field>
      <Field label="Install command" hint="Empty: the lockfile decides (npm ci, pnpm or yarn).">
        <input
          className={inputClass}
          value={settings.installCommand}
          onChange={set('installCommand')}
          data-testid="build-install"
        />
      </Field>
      <Field label="Build command" hint="Empty: npm run build.">
        <input
          className={inputClass}
          value={settings.buildCommand}
          onChange={set('buildCommand')}
          data-testid="build-command"
        />
      </Field>
      <Field label="Output directory" hint="Relative to the root directory: dist, build or out.">
        <input
          className={inputClass}
          value={settings.outputDir}
          onChange={set('outputDir')}
          data-testid="build-output"
        />
      </Field>
      <Field label="Node version" hint="The Node major the build runs on.">
        <select
          className={inputClass}
          value={settings.nodeVersion}
          onChange={set('nodeVersion')}
          data-testid="build-node"
        >
          {nodeVersions.map((version) => (
            <option key={version} value={version}>
              Node {version}
            </option>
          ))}
        </select>
      </Field>
      <div className="sm:col-span-2">
        <EnvField
          label="Build variables"
          hint="One NAME=value per line, such as VITE_API_URL. They end up in the built files anyone can download: never put a secret here."
          env={settings.env}
          testId="build-env"
          onChange={(env) => onChange({ ...settings, env })}
          onProblem={onProblem}
        />
      </div>
    </div>
  );
}
