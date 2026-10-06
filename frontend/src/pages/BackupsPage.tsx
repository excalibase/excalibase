import { useState } from 'react';
import {
  useListBackups,
  useTriggerBackup,
  useRestoreFromBackup,
  useRestoreJob,
  RESTORE_RUNNING,
  type RestoreRequest,
  type RestoreResult,
} from '../hooks/useProvisioning';
import { useRouteProjectId } from '../hooks/useRouteProjectId';
import { StatusBadge } from '../components/shared/StatusBadge';
import { Button } from '../components/Button';
import { toZonedInstant } from '../utils/zonedInstant';
import { Archive, RefreshCw, RotateCcw, Clock, type LucideIcon } from 'lucide-react';
import { serverErrorMessage } from '../utils/serverError';

type Tab = 'backups' | 'restore';

type RestoreTone = 'pending' | 'ok' | 'error';

const TONE_CLASSES: Record<RestoreTone, string> = {
  pending: 'bg-blue-900/20 border-blue-500/30 text-blue-300',
  ok: 'bg-green-900/20 border-green-500/30 text-green-400',
  error: 'bg-red-900/20 border-red-500/30 text-red-400',
};

// A started restore answers RUNNING and is polled to COMPLETED or FAILED.
function describeRestore(result: RestoreResult): { tone: RestoreTone; message: string } {
  const step = result.currentStep ? ` (${result.currentStep})` : '';
  switch (result.status) {
    case RESTORE_RUNNING:
      return { tone: 'pending', message: `Restore started. The new instance is being created${step}.` };
    case 'COMPLETED':
      return { tone: 'ok', message: 'Restore completed. The new instance is ready.' };
    case 'FAILED':
      return { tone: 'error', message: `The restore failed: ${result.failureReason || 'no reason was recorded.'}` };
    default:
      return result.failureReason
        ? { tone: 'error', message: `The restore failed: ${result.failureReason}` }
        : { tone: 'ok', message: result.message || 'Restore started.' };
  }
}

function RestoreResultPanel({ result }: { readonly result: RestoreResult }) {
  const { tone, message } = describeRestore(result);
  const newInstance = result.newProjectId ?? result.projectId;
  return (
    <div data-testid="restore-result" data-tone={tone} role="status" className={`p-3 rounded-lg border text-sm ${TONE_CLASSES[tone]}`}>
      <p className="font-medium">{message}</p>
      {newInstance && (
        <p className="mt-1 text-xs text-text-secondary">New instance: <span className="font-mono text-text-primary">{newInstance}</span></p>
      )}
      {result.recoveryType && (
        <p className="text-xs text-text-secondary">Recovery type: <span className="font-mono text-text-primary">{result.recoveryType}</span></p>
      )}
    </div>
  );
}

export function BackupsPage() {
  const projectId = useRouteProjectId();
  const [tab, setTab] = useState<Tab>('backups');

  const { data: backupData, isLoading } = useListBackups(projectId);
  const backups = Array.isArray(backupData?.backups) ? backupData.backups : [];
  const triggerBackup = useTriggerBackup();
  const [toast, setToast] = useState<{ msg: string; ok: boolean } | null>(null);

  const restore = useRestoreFromBackup(projectId);
  const [restoreForm, setRestoreForm] = useState<RestoreRequest>({ newProjectName: '', targetTime: '' });
  const isPitr = !!restoreForm.targetTime?.trim();
  const startedJobId = restore.data?.status === RESTORE_RUNNING ? restore.data.id : undefined;
  const restoreJob = useRestoreJob(projectId, startedJobId);
  const restoreResult = restoreJob.data ?? restore.data;

  function showToast(msg: string, ok: boolean) {
    setToast({ msg, ok });
    setTimeout(() => setToast(null), 4000);
  }

  return (
    <div className="max-w-5xl mx-auto space-y-6">
      {toast && (
        <div className={`px-4 py-2 rounded-lg text-sm font-medium border ${toast.ok ? 'bg-green-900/20 text-green-400 border-green-500/30' : 'bg-red-900/20 text-red-400 border-red-500/30'}`}>
          {toast.msg}
        </div>
      )}

      {backupData && (
        <div className="grid grid-cols-3 gap-4">
          <div className="bg-surface-card border border-border-primary rounded-xl p-4">
            <p className="text-xs text-text-tertiary mb-1">Backup Status</p>
            <p className={`text-sm font-semibold ${backupData.backupEnabled ? 'text-green-400' : 'text-text-secondary'}`}>
              {backupData.backupEnabled ? 'Enabled' : 'Disabled'}
            </p>
          </div>
          <div className="bg-surface-card border border-border-primary rounded-xl p-4">
            <p className="text-xs text-text-tertiary mb-1">Schedule</p>
            <p className="text-sm font-medium text-text-primary font-mono">{backupData.schedule ?? '—'}</p>
          </div>
          <div className="bg-surface-card border border-border-primary rounded-xl p-4">
            <p className="text-xs text-text-tertiary mb-1">Retention</p>
            <p className="text-sm font-medium text-text-primary">{backupData.retentionDays == null ? '—' : `${backupData.retentionDays} days`}</p>
          </div>
        </div>
      )}

      <div className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
        <div className="flex border-b border-border-primary px-4 gap-1">
          {([
            { key: 'backups', label: 'Backup History', icon: Archive },
            { key: 'restore', label: 'Restore / PITR', icon: RotateCcw },
          ] as { key: Tab; label: string; icon: LucideIcon }[]).map(({ key, label, icon: Icon }) => (
            <button
              key={key}
              onClick={() => setTab(key)}
              className={`flex items-center gap-2 px-4 py-3 text-sm font-medium border-b-2 -mb-px transition-colors ${tab === key ? 'border-accent-primary text-accent-primary' : 'border-transparent text-text-secondary hover:text-text-primary'}`}
            >
              <Icon className="w-4 h-4" />
              {label}
            </button>
          ))}
          {tab === 'backups' && (
            <div className="ml-auto flex items-center py-2">
              <Button
                size="sm"
                variant="secondary"
                disabled={!projectId || triggerBackup.isPending}
                onClick={() => triggerBackup.mutate(projectId, {
                  onSuccess: () => showToast('Backup triggered successfully', true),
                  onError: (e: unknown) => showToast(serverErrorMessage(e, 'The backup was not started'), false),
                })}
              >
                <RefreshCw className="w-4 h-4 mr-1.5" /> Trigger Backup
              </Button>
            </div>
          )}
        </div>

        <div className="p-6">
          {tab === 'backups' && isLoading && (
            <div className="text-center py-12 text-text-secondary">Loading…</div>
          )}
          {tab === 'backups' && !isLoading && backups.length === 0 && (
            <div className="text-center py-12">
              <Archive className="w-12 h-12 mx-auto mb-3 text-text-tertiary" />
              <p className="text-text-secondary">No backups found. Click "Trigger Backup" to create one.</p>
            </div>
          )}
          {tab === 'backups' && !isLoading && backups.length > 0 && (
            <table className="w-full text-sm">
              <thead>
                <tr className="text-text-tertiary border-b border-border-primary">
                  <th className="text-left py-2 font-medium">Backup ID</th>
                  <th className="text-left py-2 font-medium">Timestamp</th>
                  <th className="text-left py-2 font-medium">Type</th>
                  <th className="text-left py-2 font-medium">Size</th>
                  <th className="text-left py-2 font-medium">Status</th>
                </tr>
              </thead>
              <tbody>
                {backups.map((b) => (
                  <tr key={b.id} className="border-b border-border-primary last:border-0 hover:bg-surface-hover transition-colors">
                    <td className="py-3 font-mono text-xs text-text-primary">{b.id}</td>
                    <td className="py-3 text-text-secondary">{new Date(b.timestamp).toLocaleString()}</td>
                    <td className="py-3 text-text-tertiary">{b.type ?? 'MANUAL'}</td>
                    <td className="py-3 text-text-secondary">{b.size}</td>
                    <td className="py-3"><StatusBadge status={b.status} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {tab === 'restore' && (
            <div className="max-w-lg space-y-5">
              <p className="text-sm text-text-secondary">
                Restore creates a <strong className="text-text-primary">new database instance</strong> from this instance's backup.
                Leave the target time blank for a full restore from the latest backup, or set a time for Point-in-Time Recovery (PITR).
              </p>

              <div className="space-y-4">
                <div>
                  <label htmlFor="restore-new-name" className="text-xs text-text-secondary block mb-1">New Instance Name *</label>
                  <input
                    id="restore-new-name"
                    value={restoreForm.newProjectName}
                    onChange={(e) => setRestoreForm({ ...restoreForm, newProjectName: e.target.value })}
                    placeholder="e.g. orders restored"
                    className="w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-accent-primary"
                  />
                </div>

                <div>
                  <label htmlFor="restore-target-time" className="text-xs text-text-secondary block mb-1 flex items-center gap-1.5">
                    <Clock className="w-3 h-3" /> Target Time (PITR) — leave blank for latest backup
                  </label>
                  <input
                    id="restore-target-time"
                    type="datetime-local"
                    step={1}
                    value={restoreForm.targetTime ?? ''}
                    onChange={(e) => setRestoreForm({ ...restoreForm, targetTime: e.target.value })}
                    className="w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-accent-primary"
                  />
                  {isPitr && (
                    <p className="text-xs text-accent-primary mt-1">Point-in-Time Recovery mode enabled</p>
                  )}
                </div>
              </div>

              {restoreResult && <RestoreResultPanel result={restoreResult} />}

              <Button
                disabled={!restoreForm.newProjectName.trim() || restore.isPending || !projectId}
                onClick={() => restore.mutate(
                  // datetime-local only yields a valid local time or ''.
                  { ...restoreForm, targetTime: toZonedInstant(restoreForm.targetTime) },
                  { onError: (e: unknown) => {
                    const msg = serverErrorMessage(e, 'The restore was not started');
                    showToast(msg || 'Restore failed', false);
                  } }
                )}
              >
                <RotateCcw className="w-4 h-4 mr-2" />
                {restore.isPending && 'Initiating restore…'}
                {!restore.isPending && isPitr && 'Restore to Point in Time'}
                {!restore.isPending && !isPitr && 'Restore Latest Backup'}
              </Button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
