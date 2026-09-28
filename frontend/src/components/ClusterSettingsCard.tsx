import { useState } from 'react';
import { HardDrive, Loader2 } from 'lucide-react';
import { ConfirmModal } from './ui/ConfirmModal';
import {
  refusalMessage,
  useChangeTier,
  useClusterSettings,
  useResizeStorage,
  useTuneParameters,
  type ClusterSettings,
} from '../api/clusterSettings';
import type { DatabaseInstance } from '../types';

interface ClusterSettingsCardProps {
  readonly project: DatabaseInstance;
}

const buttonClass =
  'px-3 py-1.5 bg-accent-primary hover:opacity-90 text-white text-xs font-medium rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed';
const inputClass =
  'px-2 py-1 text-xs rounded border border-border-primary bg-surface-primary text-text-primary';

// gibibytes reads a size the control plane wrote as whole Gi ("5Gi").
function gibibytes(size: string): number {
  const match = /^(\d+)Gi$/.exec(size);
  return match ? Number(match[1]) : Number.NaN;
}

// ClusterSettingsCard lets an admin grow the disk, move the database onto the
// organization's plan and tune the allowlisted Postgres settings (EXC-492).
// Every change shows what the control plane answered, or why it refused.
export function ClusterSettingsCard({ project }: ClusterSettingsCardProps) {
  const active = project.status === 'ACTIVE';
  const settings = useClusterSettings(project.projectId, active);

  return (
    <div
      className="rounded-lg border border-border-primary bg-surface-card p-4"
      data-testid="cluster-settings-card"
    >
      <h4 className="flex items-center gap-2 text-sm font-medium text-text-primary mb-2">
        <HardDrive className="w-4 h-4" /> Size, plan and Postgres settings
      </h4>
      {!active && (
        <p className="text-xs text-text-secondary" data-testid="cluster-settings-inactive">
          The project must be active before its disk, plan or settings can change.
        </p>
      )}
      {active && settings.isLoading && <Loader2 className="w-4 h-4 animate-spin" />}
      {active && settings.isError && (
        <p className="text-xs text-color-error">{refusalMessage(settings.error)}</p>
      )}
      {active && settings.data && (
        <ClusterSettingsBody projectId={project.projectId} settings={settings.data} />
      )}
    </div>
  );
}

interface BodyProps {
  readonly projectId: string;
  readonly settings: ClusterSettings;
}

function ClusterSettingsBody({ projectId, settings }: BodyProps) {
  const resize = useResizeStorage(projectId);
  const tier = useChangeTier(projectId);
  const tune = useTuneParameters(projectId);
  const [size, setSize] = useState('');
  const [confirming, setConfirming] = useState<'resize' | 'tier' | null>(null);

  const current = gibibytes(settings.storageSize);
  const limit = gibibytes(settings.storageLimit);
  const wanted = Number(size);
  const canResize = Number.isInteger(wanted) && wanted > current && wanted <= limit;
  const failed = [resize, tier, tune].find((change) => change.isError);
  const busy = resize.isPending || tier.isPending || tune.isPending;

  return (
    <div className="space-y-4 text-xs text-text-secondary">
      <p>
        Disk:{' '}
        <span className="text-text-primary font-medium">
          {settings.storageSize} of {settings.storageLimit}
        </span>{' '}
        the plan allows. {settings.instances} {settings.instances === 1 ? 'instance' : 'instances'},{' '}
        {settings.cpu} CPU and {settings.memory} memory each. A disk can grow but never shrink.
      </p>
      <div className="flex items-center gap-2">
        <input
          type="number"
          min={current + 1}
          max={limit}
          value={size}
          onChange={(event) => setSize(event.target.value)}
          placeholder={`${current + 1}`}
          aria-label="New disk size in GiB"
          data-testid="resize-input"
          className={`${inputClass} w-20`}
        />
        <span>GiB</span>
        <button
          type="button"
          className={buttonClass}
          disabled={!canResize || busy}
          onClick={() => setConfirming('resize')}
          data-testid="resize-btn"
        >
          Grow disk
        </button>
      </div>

      <div className="flex items-center gap-2">
        <span>
          Plan: <span className="text-text-primary font-medium">{settings.tier}</span>; organization
          plan: <span className="text-text-primary font-medium">{settings.orgTier}</span>.
        </span>
        <button
          type="button"
          className={buttonClass}
          disabled={settings.tier === settings.orgTier || busy}
          onClick={() => setConfirming('tier')}
          data-testid="tier-apply-btn"
        >
          Move onto {settings.orgTier}
        </button>
      </div>

      <ParametersForm
        key={JSON.stringify(settings.parameters)}
        settings={settings}
        busy={busy}
        onSave={(parameters) => tune.mutate(parameters)}
      />

      {failed && (
        <p className="text-color-error" data-testid="cluster-settings-error">
          {refusalMessage(failed.error)}
        </p>
      )}

      <ConfirmModal
        open={confirming === 'resize'}
        onClose={() => setConfirming(null)}
        onConfirm={() => {
          setConfirming(null);
          resize.mutate(`${wanted}Gi`);
        }}
        title="Grow the disk"
        message={`The disk grows from ${settings.storageSize} to ${wanted}Gi while the database keeps running. It cannot be shrunk back.`}
        confirmLabel="Grow"
        loading={resize.isPending}
      />
      <ConfirmModal
        open={confirming === 'tier'}
        onClose={() => setConfirming(null)}
        onConfirm={() => {
          setConfirming(null);
          tier.mutate(settings.orgTier);
        }}
        title={`Move onto the ${settings.orgTier} plan`}
        message={`The database is resized to the ${settings.orgTier} plan. Instances restart one at a time, and connections drop while each restarts.`}
        confirmLabel="Move"
        loading={tier.isPending}
      />
    </div>
  );
}

interface ParametersFormProps {
  readonly settings: ClusterSettings;
  readonly busy: boolean;
  readonly onSave: (parameters: Record<string, string>) => void;
}

function ParametersForm({ settings, busy, onSave }: ParametersFormProps) {
  const [values, setValues] = useState<Record<string, string>>(settings.parameters);

  const save = () => {
    const chosen = Object.fromEntries(
      Object.entries(values).filter(([, value]) => value.trim() !== ''),
    );
    onSave(chosen);
  };

  return (
    <div>
      <p className="mb-2">
        Postgres settings you may tune; empty means the default. Values are checked against the
        plan.
      </p>
      <div className="grid grid-cols-2 gap-2">
        {settings.tunableParameters.map((name) => (
          <label
            key={name}
            className="flex items-center justify-between gap-2"
            data-testid={`param-${name}`}
          >
            <span className="font-mono">{name}</span>
            <input
              type="text"
              value={values[name] ?? ''}
              onChange={(event) => setValues({ ...values, [name]: event.target.value })}
              className={`${inputClass} w-32`}
            />
          </label>
        ))}
      </div>
      <button
        type="button"
        className={`${buttonClass} mt-2`}
        disabled={busy}
        onClick={save}
        data-testid="params-save-btn"
      >
        Save settings
      </button>
    </div>
  );
}
