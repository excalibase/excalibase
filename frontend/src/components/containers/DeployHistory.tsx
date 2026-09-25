import { useState } from 'react';
import { RotateCcw } from 'lucide-react';
import type { Deploy } from '../../api/apps';
import { DEPLOY_STATUS, formatWhen } from './appCopy';
import { ToneBadge, primaryButton, secondaryButton } from './ContainerBits';

interface DeployHistoryProps {
  readonly deploys: Deploy[];
  readonly redeploying: boolean;
  readonly onRedeploy: (deployId: string) => void;
}

interface RowProps {
  readonly deploy: Deploy;
  readonly isNewest: boolean;
  readonly confirming: boolean;
  readonly redeploying: boolean;
  readonly onAsk: () => void;
  readonly onCancel: () => void;
  readonly onConfirm: () => void;
}

function DeployRow({
  deploy,
  isNewest,
  confirming,
  redeploying,
  onAsk,
  onCancel,
  onConfirm,
}: RowProps) {
  const status = DEPLOY_STATUS[deploy.status];
  return (
    <li
      className="px-4 py-3 border-b border-border-primary last:border-b-0"
      data-testid={`deploy-row-${deploy.id}`}
    >
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-3 min-w-0">
          <span className="text-sm font-mono text-text-primary">#{deploy.revision}</span>
          <span className="text-xs font-mono text-text-tertiary truncate">{deploy.image}</span>
        </div>
        <div className="flex items-center gap-3 flex-shrink-0">
          <span className="text-xs text-text-tertiary">{formatWhen(deploy.createdAt)}</span>
          <ToneBadge label={status.label} tone={status.tone} />
          {!isNewest && !confirming && (
            <button
              type="button"
              onClick={onAsk}
              className={secondaryButton}
              data-testid={`redeploy-${deploy.id}`}
            >
              <RotateCcw className="w-3.5 h-3.5" /> Redeploy
            </button>
          )}
        </div>
      </div>
      {confirming && (
        <div className="mt-3 flex items-center justify-between gap-3 rounded-lg border border-purple-500/30 bg-purple-500/5 px-3 py-2">
          <p className="text-sm text-text-primary">
            Roll back to revision {deploy.revision}? It runs {deploy.image} again with the settings
            it had then.
          </p>
          <div className="flex items-center gap-2 flex-shrink-0">
            <button
              type="button"
              onClick={onCancel}
              className={secondaryButton}
              data-testid={`redeploy-cancel-${deploy.id}`}
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={onConfirm}
              disabled={redeploying}
              className={primaryButton}
              data-testid={`redeploy-confirm-${deploy.id}`}
            >
              Redeploy
            </button>
          </div>
        </div>
      )}
    </li>
  );
}

export function DeployHistory({ deploys, redeploying, onRedeploy }: DeployHistoryProps) {
  const [confirmingId, setConfirmingId] = useState<string | null>(null);

  if (deploys.length === 0) {
    return (
      <p className="px-4 py-6 text-sm text-text-tertiary">
        No deploys yet. Press Deploy to start the container.
      </p>
    );
  }
  return (
    <ul data-testid="deploy-history">
      {deploys.map((deploy, index) => (
        <DeployRow
          key={deploy.id}
          deploy={deploy}
          isNewest={index === 0}
          confirming={confirmingId === deploy.id}
          redeploying={redeploying}
          onAsk={() => setConfirmingId(deploy.id)}
          onCancel={() => setConfirmingId(null)}
          onConfirm={() => {
            setConfirmingId(null);
            onRedeploy(deploy.id);
          }}
        />
      ))}
    </ul>
  );
}
