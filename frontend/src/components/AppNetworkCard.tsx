import { useState } from 'react';
import { Loader2, Lock, Network } from 'lucide-react';
import { useAppNetwork, useSetAppNetwork } from '../api/appNetwork';
import { refusalMessage } from '../api/clusterSettings';
import { Button } from './Button';
import { useAppHostingEnabled } from '../hooks/useDeploymentMode';

interface AppNetworkCardProps {
  readonly projectId: string;
  readonly status: string;
}

const OFF_DEFAULT =
  'Off by default: each app accepts traffic only from its public URL. ' +
  'Turn this on to let this project’s apps call each other by name. Apps in other projects, ' +
  'the database and the internet never gain access through it.';

// AppNetworkCard turns the project's private network between its apps on or
// off (EXC-524). The control plane decides; the card shows its answer.
export function AppNetworkCard({ projectId, status }: AppNetworkCardProps) {
  const network = useAppNetwork(projectId);
  const change = useSetAppNetwork(projectId);
  const [confirming, setConfirming] = useState(false);
  const current = network.data;
  const canChange = current?.canChange === true;
  const active = status === 'ACTIVE';

  const toggle = () => {
    if (!current) return;
    if (current.privateNetwork) {
      change.mutate(false);
      return;
    }
    setConfirming(true);
  };

  const turnOn = () => {
    setConfirming(false);
    change.mutate(true);
  };

  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4" data-testid="app-network-card">
      <h4 className="flex items-center gap-2 text-sm font-medium text-text-primary mb-2">
        <Network className="w-4 h-4" /> Private network between apps
      </h4>
      <p className="text-xs text-text-secondary mb-3">{OFF_DEFAULT}</p>

      {network.isLoading && <Loader2 className="w-4 h-4 animate-spin" />}
      {network.isError && (
        <p className="text-xs text-text-tertiary">The setting could not be read: {refusalMessage(network.error)}</p>
      )}
      {current && (
        <div className="flex items-center justify-between gap-4">
          <p className="flex items-center gap-2 text-sm text-text-primary" data-testid="app-network-state">
            {current.privateNetwork ? (
              <>
                <Network className="w-4 h-4 text-amber-400" /> On — apps reach each other at{' '}
                <code className="font-mono text-xs">http://&lt;app name&gt;</code>
              </>
            ) : (
              <>
                <Lock className="w-4 h-4 text-green-400" /> Off — apps cannot reach each other
              </>
            )}
          </p>
          {canChange && (
            <Button
              size="sm"
              variant={current.privateNetwork ? 'secondary' : 'primary'}
              disabled={change.isPending || confirming || (!current.privateNetwork && !active)}
              onClick={toggle}
              data-testid="app-network-toggle"
            >
              {current.privateNetwork ? 'Turn off' : 'Turn on'}
            </Button>
          )}
        </div>
      )}
      {current && current.privateNetwork !== current.applied && (
        <p className="text-xs text-color-error mt-2" data-testid="app-network-not-applied">
          The cluster does not match this setting right now. Set it again to apply it.
        </p>
      )}
      {current && canChange && !active && !current.privateNetwork && (
        <p className="text-xs text-text-tertiary mt-2">The project must be active before this can be turned on.</p>
      )}
      {current && !canChange && (
        <p className="text-xs text-text-tertiary mt-2" data-testid="app-network-admin-only">
          Only an org admin or owner can change this.
        </p>
      )}
      {confirming && (
        <div className="mt-3 rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-text-primary">
          <p>
            Every app in this project will be able to connect to every other app in it. A compromised app
            could then reach the others directly, not only through their public URLs.
          </p>
          <div className="mt-2 flex gap-2">
            <Button size="sm" variant="danger" onClick={turnOn} data-testid="app-network-confirm">
              Turn on
            </Button>
            <Button size="sm" variant="secondary" onClick={() => setConfirming(false)}>
              Cancel
            </Button>
          </div>
        </div>
      )}
      {change.isError && (
        <p role="alert" className="text-xs text-color-error mt-2">
          {refusalMessage(change.error)}
        </p>
      )}
    </div>
  );
}

// AppNetworkSection shows the card only where the installation hosts apps.
export function AppNetworkSection({ projectId, status }: AppNetworkCardProps) {
  const { enabled } = useAppHostingEnabled();
  if (!enabled) return null;
  return (
    <div className="mb-8">
      <AppNetworkCard projectId={projectId} status={status} />
    </div>
  );
}
