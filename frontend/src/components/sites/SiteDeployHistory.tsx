import { useState } from 'react';
import { RotateCcw } from 'lucide-react';
import type { SiteDeploy } from '../../api/sites';
import { formatBytes } from '../../utils/formatBytes';
import { formatWhen } from '../containers/appCopy';
import { ToneBadge, primaryButton, secondaryButton } from '../containers/ContainerBits';
import { deployStatus } from './SiteBits';

interface HistoryProps {
  readonly deploys: SiteDeploy[];
  readonly rollingBack: boolean;
  // Absent for a caller who may not change the site: no rollback is offered.
  readonly onRollback?: (deployId: string) => void;
}

const fileCount = (count: number) => `${count} ${count === 1 ? 'file' : 'files'}`;

function RollbackConfirm({
  deploy,
  rollingBack,
  onCancel,
  onConfirm,
}: {
  readonly deploy: SiteDeploy;
  readonly rollingBack: boolean;
  readonly onCancel: () => void;
  readonly onConfirm: () => void;
}) {
  return (
    <div className="mt-3 flex items-center justify-between gap-3 rounded-lg border border-purple-500/30 bg-purple-500/5 px-3 py-2">
      <p className="text-sm text-text-primary">
        Make the deploy of {formatWhen(deploy.createdAt)} live again? It serves those files with the
        routing it was published with.
      </p>
      <div className="flex items-center gap-2 flex-shrink-0">
        <button
          type="button"
          onClick={onCancel}
          className={secondaryButton}
          data-testid={`site-rollback-cancel-${deploy.id}`}
        >
          Cancel
        </button>
        <button
          type="button"
          onClick={onConfirm}
          disabled={rollingBack}
          className={primaryButton}
          data-testid={`site-rollback-confirm-${deploy.id}`}
        >
          Roll back
        </button>
      </div>
    </div>
  );
}

function DeployRow({
  deploy,
  confirming,
  rollingBack,
  mayRollBack,
  onAsk,
  onCancel,
  onConfirm,
}: {
  readonly deploy: SiteDeploy;
  readonly confirming: boolean;
  readonly rollingBack: boolean;
  readonly mayRollBack: boolean;
  readonly onAsk: () => void;
  readonly onCancel: () => void;
  readonly onConfirm: () => void;
}) {
  const status = deployStatus(deploy);
  const canRollBack = mayRollBack && deploy.status === 'SUCCEEDED' && !deploy.live;
  return (
    <li
      className="px-4 py-3 border-b border-border-primary last:border-b-0"
      data-testid={`site-deploy-row-${deploy.id}`}
    >
      <div className="flex items-center justify-between gap-4">
        <div className="min-w-0 space-y-0.5">
          <p className="text-sm text-text-primary">
            {formatWhen(deploy.createdAt)}
            {deploy.label && (
              <span className="ml-2 font-mono text-xs text-text-tertiary">{deploy.label}</span>
            )}
          </p>
          <p className="text-xs text-text-tertiary">
            {formatBytes(deploy.totalBytes)} · {fileCount(deploy.fileCount)}
            {deploy.createdByName && ` · by ${deploy.createdByName}`}
            {deploy.deployTokenName && ` · with deploy token ${deploy.deployTokenName}`}
          </p>
        </div>
        <div className="flex items-center gap-3 flex-shrink-0">
          <ToneBadge {...status} testId={`site-deploy-status-${deploy.id}`} />
          {canRollBack && !confirming && (
            <button
              type="button"
              onClick={onAsk}
              className={secondaryButton}
              data-testid={`site-rollback-${deploy.id}`}
            >
              <RotateCcw className="w-3.5 h-3.5" /> Roll back
            </button>
          )}
        </div>
      </div>
      {deploy.status === 'FAILED' && deploy.failureReason && (
        <p className="mt-2 text-xs text-red-400">{deploy.failureReason}</p>
      )}
      {confirming && (
        <RollbackConfirm
          deploy={deploy}
          rollingBack={rollingBack}
          onCancel={onCancel}
          onConfirm={onConfirm}
        />
      )}
    </li>
  );
}

export function SiteDeployHistory({ deploys, rollingBack, onRollback }: HistoryProps) {
  const [confirmingId, setConfirmingId] = useState<string | null>(null);
  if (deploys.length === 0) {
    return (
      <p className="px-4 py-6 text-sm text-text-tertiary" data-testid="site-deploys-empty">
        No deploys yet. Upload your build output above, or deploy from CI, to put the site live.
      </p>
    );
  }
  return (
    <ul data-testid="site-deploy-history">
      {deploys.map((deploy) => (
        <DeployRow
          key={deploy.id}
          deploy={deploy}
          confirming={confirmingId === deploy.id}
          rollingBack={rollingBack}
          mayRollBack={onRollback !== undefined}
          onAsk={() => setConfirmingId(deploy.id)}
          onCancel={() => setConfirmingId(null)}
          onConfirm={() => {
            setConfirmingId(null);
            onRollback?.(deploy.id);
          }}
        />
      ))}
    </ul>
  );
}
