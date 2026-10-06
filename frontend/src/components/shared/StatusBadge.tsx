import { CheckCircle, AlertTriangle, XCircle, Loader2, MinusCircle, PauseCircle, Trash2, HelpCircle } from 'lucide-react';
import { cn } from '../../utils/cn';
import type { ProvisioningStage } from '../../types';

interface StatusBadgeProps {
  // The record's own status (project, backup, ...). Wins over the stage unless
  // the project is still provisioning, when the pipeline stage says more.
  status?: string;
  stage?: ProvisioningStage;
  // When a PENDING_DELETION project is permanently deleted.
  deletionDueAt?: string;
  className?: string;
}

type Tone = 'red' | 'green' | 'amber' | 'blue' | 'neutral';

const TONES: Record<Tone, string> = {
  red: 'bg-red-500/10 text-red-400 border-red-500/30',
  green: 'bg-green-500/10 text-green-400 border-green-500/30',
  amber: 'bg-amber-500/10 text-amber-400 border-amber-500/30',
  blue: 'bg-blue-500/10 text-blue-400 border-blue-500/30',
  neutral: 'bg-bg-tertiary text-text-tertiary border-border-secondary',
};

const icon = (Icon: typeof CheckCircle, spin = false) => <Icon className={cn('w-3 h-3', spin && 'animate-spin')} />;

interface Look { tone: Tone; label: string; Icon: typeof CheckCircle; spin?: boolean }

// Pipeline stages a project passes through while it is being built.
const BUILD_STAGES = new Set([
  'PROVISIONING', 'VALIDATING', 'NAMESPACE_CREATION', 'CRD_DEPLOYMENT', 'WAITING_FOR_READY',
  'CREDENTIAL_GENERATION', 'BACKUP_CONFIGURATION', 'METRICS_SETUP', 'WATCHER_DEPLOYMENT',
  'CONTAINER_CREATION', 'ROLE_CREATION',
]);

const LOOKS: Record<string, Look> = {
  FAILED: { tone: 'red', label: 'Failed', Icon: XCircle },
  ACTIVE: { tone: 'green', label: 'Active', Icon: CheckCircle },
  HEALTHY: { tone: 'green', label: 'Healthy', Icon: CheckCircle },
  DEGRADED: { tone: 'amber', label: 'Degraded', Icon: AlertTriangle },
  DOWN: { tone: 'red', label: 'Down', Icon: XCircle },
  DEPROVISIONED: { tone: 'neutral', label: 'Deprovisioned', Icon: MinusCircle },
  PAUSED: { tone: 'amber', label: 'Paused', Icon: PauseCircle },
  PAUSING: { tone: 'blue', label: 'Pausing', Icon: Loader2, spin: true },
  RESUMING: { tone: 'blue', label: 'Resuming', Icon: Loader2, spin: true },
  DELETING: { tone: 'blue', label: 'Deleting', Icon: Loader2, spin: true },
  RESTORING: { tone: 'blue', label: 'Restoring', Icon: Loader2, spin: true },
  IN_PROGRESS: { tone: 'blue', label: 'In progress', Icon: Loader2, spin: true },
  RUNNING: { tone: 'blue', label: 'In progress', Icon: Loader2, spin: true },
  BACKUPS_PENDING_DELETE: { tone: 'neutral', label: 'Deleted, backups pending purge', Icon: Trash2 },
  PENDING_DELETION: { tone: 'red', label: 'Scheduled for deletion', Icon: Trash2 },
};

function formatDue(iso?: string): string {
  if (!iso) return '';
  const due = new Date(iso);
  return Number.isNaN(due.getTime()) ? '' : ` · ${due.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })}`;
}

function lookFor(status: string | undefined, stage: string | undefined): Look | { unknown: string } {
  const useStage = !status || status === 'PROVISIONING';
  const value = useStage ? (stage ?? status ?? '') : status;
  if (value === 'COMPLETED') {
    // A finished pipeline is a working project; a finished backup is just done.
    return useStage && stage ? LOOKS.ACTIVE : { tone: 'green', label: 'Completed', Icon: CheckCircle };
  }
  if (LOOKS[value]) return LOOKS[value];
  if (BUILD_STAGES.has(value)) return { tone: 'blue', label: 'Provisioning', Icon: Loader2, spin: true };
  return { unknown: value };
}

export function StatusBadge({ status, stage, deletionDueAt, className }: StatusBadgeProps) {
  const look = lookFor(status, stage);
  const badge = (tone: Tone, content: React.ReactNode, label: string) => (
    <span className={cn('inline-flex items-center gap-1 px-2.5 py-1 text-xs font-medium rounded-full border', TONES[tone], className)}>
      {content} {label}
    </span>
  );
  if ('unknown' in look) {
    return badge('neutral', icon(HelpCircle), look.unknown ? `Unknown (${look.unknown})` : 'Unknown');
  }
  const suffix = look === LOOKS.PENDING_DELETION ? formatDue(deletionDueAt) : '';
  return badge(look.tone, icon(look.Icon, look.spin), look.label + suffix);
}
