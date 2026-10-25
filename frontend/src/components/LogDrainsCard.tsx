import { useState } from 'react';
import { Pencil, Power, Trash2 } from 'lucide-react';
import {
  MAX_LOG_DRAINS,
  drainDestination,
  useCreateLogDrain,
  useDeleteLogDrain,
  useLogDrains,
  useUpdateLogDrain,
  type LogDrain,
  type LogDrainStatus,
} from '../api/logDrains';
import { useFeatureEnabled } from '../hooks/useDeploymentMode';
import { useProjectRole } from '../hooks/useProjectRole';
import { serverErrorMessage } from '../utils/serverError';
import { ConfirmModal } from './ui/ConfirmModal';
import { formatWhen, type Tone } from './containers/appCopy';
import { Spinner, ToneBadge, secondaryButton } from './containers/ContainerBits';
import { DrainForm, createBody, emptyDrainValues, updateBody, valuesOf } from './LogDrainForm';

interface ProjectRef {
  readonly projectId: string;
}

const STATUS_TONE: Record<LogDrainStatus, Tone> = {
  active: 'success',
  failing: 'warning',
  disabled: 'neutral',
};

const isForbidden = (error: unknown) =>
  (error as { response?: { status?: number } } | null)?.response?.status === 403;

function DrainRow({
  drain,
  onToggle,
  onEdit,
  onDelete,
}: {
  readonly drain: LogDrain;
  readonly onToggle: () => void;
  readonly onEdit: () => void;
  readonly onDelete: () => void;
}) {
  const showError = drain.status !== 'active' && drain.lastError;
  return (
    <li
      className="flex flex-wrap items-start justify-between gap-3 px-3 py-2"
      data-testid={`log-drain-${drain.id}`}
    >
      <div className="min-w-0 space-y-0.5">
        <p className="text-sm text-text-primary flex items-center gap-2">
          <span className="truncate">{drain.name}</span>
          <ToneBadge label={drain.status} tone={STATUS_TONE[drain.status]} />
        </p>
        <p className="text-xs text-text-tertiary break-all">
          {drain.kind === 'datadog' ? 'Datadog' : 'HTTPS'} · {drainDestination(drain)}
        </p>
        <p className="text-xs text-text-tertiary">
          {drain.sentLines.toLocaleString()} sent · {drain.droppedLines.toLocaleString()} dropped ·{' '}
          {drain.lastSuccessAt
            ? `Last sent ${formatWhen(drain.lastSuccessAt)}`
            : 'Nothing sent yet'}
        </p>
        {showError && <p className="text-xs text-amber-400 break-all">{drain.lastError}</p>}
      </div>
      <div className="flex gap-2">
        <button
          type="button"
          onClick={onToggle}
          className={secondaryButton}
          data-testid={`log-drain-toggle-${drain.id}`}
        >
          <Power className="w-3.5 h-3.5" /> {drain.enabled ? 'Disable' : 'Enable'}
        </button>
        <button
          type="button"
          onClick={onEdit}
          className={secondaryButton}
          data-testid={`log-drain-edit-${drain.id}`}
        >
          <Pencil className="w-3.5 h-3.5" /> Edit
        </button>
        <button
          type="button"
          onClick={onDelete}
          className={secondaryButton}
          data-testid={`log-drain-delete-${drain.id}`}
        >
          <Trash2 className="w-3.5 h-3.5" /> Delete
        </button>
      </div>
    </li>
  );
}

function AddDrain({ projectId, atLimit }: ProjectRef & { readonly atLimit: boolean }) {
  const create = useCreateLogDrain(projectId);
  return (
    <DrainForm
      initial={emptyDrainValues}
      submitLabel={atLimit ? `Add (at most ${MAX_LOG_DRAINS})` : 'Add'}
      disabled={atLimit}
      pending={create.isPending}
      serverError={
        create.error ? serverErrorMessage(create.error, 'The drain could not be added') : undefined
      }
      onSubmit={(values, clear) => create.mutate(createBody(values), { onSuccess: clear })}
    />
  );
}

function EditDrain({
  projectId,
  drain,
  onDone,
}: ProjectRef & { readonly drain: LogDrain; readonly onDone: () => void }) {
  const update = useUpdateLogDrain(projectId);
  return (
    <DrainForm
      initial={valuesOf(drain)}
      current={drain}
      submitLabel="Save"
      pending={update.isPending}
      serverError={
        update.error ? serverErrorMessage(update.error, 'The drain could not be saved') : undefined
      }
      onSubmit={(values) =>
        update.mutate({ id: drain.id, changes: updateBody(values, drain) }, { onSuccess: onDone })
      }
      onCancel={onDone}
    />
  );
}

function LogDrains({ projectId }: ProjectRef) {
  const { data: drains, error } = useLogDrains(projectId, true);
  const toggle = useUpdateLogDrain(projectId);
  const remove = useDeleteLogDrain(projectId);
  const [editing, setEditing] = useState<LogDrain | null>(null);
  const [deleting, setDeleting] = useState<LogDrain | null>(null);
  const actionError = toggle.error ?? remove.error;

  return (
    <section
      className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-3 mt-6"
      data-testid="log-drains"
    >
      <h3 className="text-sm font-semibold text-text-primary flex items-center gap-2">
        <span>Log forwarding</span>
        <span className="px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide rounded border border-purple-500/30 bg-purple-500/10 text-purple-400">
          Beta
        </span>
      </h3>
      <p className="text-xs text-text-tertiary">
        Forwards this project&apos;s database logs to Datadog or an HTTPS endpoint, to at most{' '}
        {MAX_LOG_DRAINS} destinations. Forwarding starts from now; there is no backfill.
      </p>
      {error && isForbidden(error) && (
        <p className="text-sm text-text-secondary">Only project Admins manage log forwarding.</p>
      )}
      {error && !isForbidden(error) && (
        <p role="alert" className="text-xs text-red-400">
          {serverErrorMessage(error, 'Could not load the log drains')}
        </p>
      )}
      {!drains && !error && <Spinner />}
      {drains?.length === 0 && <p className="text-sm text-text-secondary">No log drains yet.</p>}
      {drains && drains.length > 0 && (
        <ul className="divide-y divide-border-primary border border-border-primary rounded-lg">
          {drains.map((drain) => (
            <DrainRow
              key={drain.id}
              drain={drain}
              onToggle={() => toggle.mutate({ id: drain.id, changes: { enabled: !drain.enabled } })}
              onEdit={() => setEditing(drain)}
              onDelete={() => setDeleting(drain)}
            />
          ))}
        </ul>
      )}
      {actionError && (
        <p role="alert" className="text-xs text-red-400">
          {serverErrorMessage(actionError, 'The drain could not be changed')}
        </p>
      )}
      {editing && (
        <EditDrain
          key={editing.id}
          projectId={projectId}
          drain={editing}
          onDone={() => setEditing(null)}
        />
      )}
      {drains && !editing && (
        <AddDrain projectId={projectId} atLimit={drains.length >= MAX_LOG_DRAINS} />
      )}
      <ConfirmModal
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        onConfirm={() =>
          deleting
            ? remove.mutateAsync(deleting.id).then(
                () => setDeleting(null),
                () => setDeleting(null),
              )
            : undefined
        }
        title={`Delete ${deleting?.name ?? 'this drain'}?`}
        message="Logs stop going to this destination and its stored secret is removed."
        confirmLabel="Delete drain"
        destructive
        loading={remove.isPending}
      />
    </section>
  );
}

function AdminsOnly({ projectId }: ProjectRef) {
  const { canAdmin } = useProjectRole(projectId);
  return canAdmin ? <LogDrains projectId={projectId} /> : null;
}

// Shown to project Admins while the logdrains feature is on (EXC-658);
// while it is off nothing beyond the Studio config is read.
export function LogDrainsCard({ projectId }: ProjectRef) {
  const { enabled } = useFeatureEnabled('logdrains');
  return enabled ? <AdminsOnly projectId={projectId} /> : null;
}
