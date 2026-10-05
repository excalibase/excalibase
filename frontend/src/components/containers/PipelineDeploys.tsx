import { useState } from 'react';
import { RotateCcw } from 'lucide-react';
import type { Deploy } from '../../api/apps';
import { DEPLOY_STATUS, formatWhen, plainFailureReason } from './appCopy';
import { ToneBadge, primaryButton, secondaryButton } from './ContainerBits';
import { deployDuration, deployImageLabel, deploySourceLabel, shortDigest } from './pipelineCopy';

interface PipelineDeploysProps {
  readonly deploys: Deploy[];
  readonly busy: boolean;
  readonly onRedeploy: (deployId: string) => void;
}

function RollbackConfirm({
  deploy,
  busy,
  onCancel,
  onConfirm,
}: {
  readonly deploy: Deploy;
  readonly busy: boolean;
  readonly onCancel: () => void;
  readonly onConfirm: () => void;
}) {
  const what = deploy.digest ? `digest ${shortDigest(deploy.digest)}` : deploy.image;
  return (
    <div className="mt-3 flex items-center justify-between gap-3 rounded-lg border border-purple-500/30 bg-purple-500/5 px-3 py-2">
      <p className="text-sm text-text-primary" data-testid="rollback-confirm-text">
        Roll back to revision {deploy.revision}? It runs {what} again with the settings it had then.
        Database migrations are not touched: a schema change made since stays.
      </p>
      <div className="flex items-center gap-2 flex-shrink-0">
        <button type="button" onClick={onCancel} className={secondaryButton}>
          Cancel
        </button>
        <button
          type="button"
          onClick={onConfirm}
          disabled={busy}
          className={primaryButton}
          data-testid={`rollback-confirm-${deploy.id}`}
        >
          Roll back
        </button>
      </div>
    </div>
  );
}

function DeployRow({
  deploy,
  isNewest,
  isServing,
  busy,
  confirming,
  onAsk,
  onCancel,
  onRedeploy,
}: {
  readonly deploy: Deploy;
  readonly isNewest: boolean;
  readonly isServing: boolean;
  readonly busy: boolean;
  readonly confirming: boolean;
  readonly onAsk: () => void;
  readonly onCancel: () => void;
  readonly onRedeploy: () => void;
}) {
  const status =
    DEPLOY_STATUS[deploy.status === 'succeeded' && !isServing ? 'superseded' : deploy.status];
  return (
    <li className="px-4 py-3" data-testid={`pipeline-deploy-${deploy.id}`}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
        <span className="font-mono text-text-primary w-10">#{deploy.revision}</span>
        <span className="text-text-secondary w-32" data-testid="deploy-source">
          {deploySourceLabel(deploy)}
        </span>
        <span className="font-mono text-xs text-text-tertiary flex-1 min-w-0 truncate" data-testid="deploy-image">
          {deployImageLabel(deploy)}
        </span>
        <span className="text-xs text-text-tertiary w-16 text-right" data-testid="deploy-duration">
          {deployDuration(deploy)}
        </span>
        <span className="text-xs text-text-tertiary">{formatWhen(deploy.createdAt)}</span>
        <ToneBadge label={status.label} tone={status.tone} />
        {isNewest ? (
          <button
            type="button"
            onClick={onRedeploy}
            disabled={busy}
            className={secondaryButton}
            data-testid={`redeploy-${deploy.id}`}
          >
            <RotateCcw className="w-3.5 h-3.5" /> Redeploy
          </button>
        ) : (
          !confirming && (
            <button
              type="button"
              onClick={onAsk}
              disabled={busy}
              className={secondaryButton}
              data-testid={`rollback-${deploy.id}`}
            >
              <RotateCcw className="w-3.5 h-3.5" /> Roll back
            </button>
          )
        )}
      </div>
      {deploy.status === 'failed' && (
        <div className="mt-2 space-y-1">
          <p className="text-sm text-red-400">{plainFailureReason(deploy.failureReason)}</p>
          {deploy.failureReason && (
            <p className="text-xs font-mono text-text-tertiary break-all">Details: {deploy.failureReason}</p>
          )}
        </div>
      )}
      {confirming && (
        <RollbackConfirm deploy={deploy} busy={busy} onCancel={onCancel} onConfirm={onRedeploy} />
      )}
    </li>
  );
}

export function PipelineDeploys({ deploys, busy, onRedeploy }: PipelineDeploysProps) {
  const [confirmingId, setConfirmingId] = useState<string | null>(null);
  const servingId = deploys.find((deploy) => deploy.status === 'succeeded')?.id;
  if (deploys.length === 0) {
    return (
      <p className="px-4 py-6 text-sm text-text-tertiary">
        No deploys yet. Deploy from Studio, from CI with the setup below, or switch auto-deploy on.
      </p>
    );
  }
  return (
    <ul className="divide-y divide-border-primary" data-testid="pipeline-deploys">
      {deploys.map((deploy, index) => (
        <DeployRow
          key={deploy.id}
          deploy={deploy}
          isNewest={index === 0}
          isServing={deploy.id === servingId}
          busy={busy}
          confirming={confirmingId === deploy.id}
          onAsk={() => setConfirmingId(deploy.id)}
          onCancel={() => setConfirmingId(null)}
          onRedeploy={() => {
            setConfirmingId(null);
            onRedeploy(deploy.id);
          }}
        />
      ))}
    </ul>
  );
}
