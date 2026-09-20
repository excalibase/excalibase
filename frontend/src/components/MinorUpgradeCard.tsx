import { useState } from 'react';
import { ArrowUpCircle, Loader2 } from 'lucide-react';
import { useUpgradeMinorVersion } from '../hooks/useProvisioning';
import { ConfirmModal } from './ui/ConfirmModal';
import type { DatabaseInstance } from '../types';

interface MinorUpgradeCardProps {
  readonly project: DatabaseInstance;
}

// MinorUpgradeCard offers the one version change a running project can take:
// the newest patch of the major it already runs. Moving between majors is a
// different operation and is deliberately not offered here.
//
// It is a lifecycle action, so it sits behind the same confirmation as pause
// and delete, and it reports the state the control plane answered with. The
// patch starts a rolling restart that outlives the request, so a card that
// repainted "done" on a 200 would be claiming something nobody observed.
export function MinorUpgradeCard({ project }: MinorUpgradeCardProps) {
  const [confirming, setConfirming] = useState(false);
  const upgrade = useUpgradeMinorVersion();

  const major = project.postgresVersion;
  const blocked = project.status !== 'ACTIVE' || !major;

  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4" data-testid="minor-upgrade-card">
      <h4 className="text-sm font-medium text-text-primary mb-2">PostgreSQL Version</h4>
      <p className="text-xs text-text-secondary mb-3">
        {major
          ? `This project runs PostgreSQL ${major}. A minor upgrade takes the newest patch release of ${major} — the major does not change.`
          : 'This project records no PostgreSQL major, so its newest patch cannot be resolved.'}
        {project.status !== 'ACTIVE' && ' The project must be active before it can be upgraded.'}
      </p>

      <button
        type="button"
        onClick={() => setConfirming(true)}
        disabled={blocked || upgrade.isPending}
        data-testid="minor-upgrade-btn"
        className="flex items-center gap-2 px-4 py-2 bg-accent-primary hover:opacity-90 text-white text-sm font-medium rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
      >
        {upgrade.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <ArrowUpCircle className="w-4 h-4" />}
        Upgrade to newest patch
      </button>

      {upgrade.isError && (
        <p className="text-xs text-color-error mt-3" data-testid="minor-upgrade-error">
          {upgrade.error instanceof Error ? upgrade.error.message : String(upgrade.error)}
        </p>
      )}

      {upgrade.isSuccess && upgrade.data && (
        <p className="text-xs text-text-secondary mt-3" data-testid="minor-upgrade-result">
          Upgrade requested. The project reports status {upgrade.data.status} on PostgreSQL {upgrade.data.postgresVersion}
          {upgrade.data.currentStage ? ` (${upgrade.data.currentStage})` : ''}. The restart is rolling; the project page
          shows it as it settles.
        </p>
      )}

      <ConfirmModal
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() => {
          setConfirming(false);
          upgrade.mutate(project.projectId);
        }}
        title="Upgrade to the newest patch"
        message={`This restarts the database for "${project.projectId}" onto the newest patch release of PostgreSQL ${major}. Connections drop while each instance restarts.`}
        confirmLabel="Upgrade"
        loading={upgrade.isPending}
      />
    </div>
  );
}
