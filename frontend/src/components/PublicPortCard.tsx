import { useState } from 'react';
import { Globe, Loader2, Lock } from 'lucide-react';
import { useProjectEndpoint, useSetProjectEndpointPublic } from '../api/projectEndpoint';
import { refusalMessage } from '../api/clusterSettings';
import { Button } from './Button';

interface PublicPortCardProps {
  readonly projectId: string;
  readonly status: string;
}

const PRIVATE_DEFAULT =
  'Private by default: the database answers only inside this project, which is where the API and anything you host beside it connect. ' +
  'It is a raw, password-protected port with no application in front of it, so a public port is opt-in.';

const notOffered = (error: unknown): boolean =>
  (error as { response?: { status?: number } } | null)?.response?.status === 503;

// PublicPortCard opens or closes the database's public port (EXC-410). The
// control plane decides everything; the card shows its answer or its refusal.
export function PublicPortCard({ projectId, status }: PublicPortCardProps) {
  const endpoint = useProjectEndpoint(projectId);
  const change = useSetProjectEndpointPublic(projectId);
  const [confirming, setConfirming] = useState(false);
  const active = status === 'ACTIVE';
  const current = endpoint.data;

  const toggle = () => {
    if (!current) return;
    if (current.publicEnabled) {
      change.mutate(false);
      return;
    }
    setConfirming(true);
  };

  const open = () => {
    setConfirming(false);
    change.mutate(true);
  };

  return (
    <div
      className="rounded-lg border border-border-primary bg-surface-card p-4"
      data-testid="public-port-card"
    >
      <h4 className="flex items-center gap-2 text-sm font-medium text-text-primary mb-2">
        <Globe className="w-4 h-4" /> Public database port
      </h4>
      <p className="text-xs text-text-secondary mb-3">{PRIVATE_DEFAULT}</p>

      {endpoint.isLoading && <Loader2 className="w-4 h-4 animate-spin" />}
      {endpoint.isError && (
        <p className="text-xs text-text-tertiary" data-testid="public-port-unavailable">
          {notOffered(endpoint.error)
            ? 'This installation does not offer public database ports.'
            : `The public port could not be read: ${refusalMessage(endpoint.error)}`}
        </p>
      )}
      {current && (
        <div className="flex items-center justify-between gap-4">
          <p
            className="flex items-center gap-2 text-sm text-text-primary"
            data-testid="public-port-state"
          >
            {current.publicEnabled ? (
              <>
                <Globe className="w-4 h-4 text-amber-400" />
                Open at{' '}
                <code className="font-mono text-xs">
                  {current.host}:{current.port}
                </code>
                {!current.available && (
                  <span className="text-xs text-text-tertiary">(not answering right now)</span>
                )}
              </>
            ) : (
              <>
                <Lock className="w-4 h-4 text-green-400" /> Private — no public port
              </>
            )}
          </p>
          <Button
            size="sm"
            variant={current.publicEnabled ? 'secondary' : 'primary'}
            disabled={!active || change.isPending || confirming}
            onClick={toggle}
            data-testid="public-port-toggle"
          >
            {current.publicEnabled ? 'Close public port' : 'Open public port'}
          </Button>
        </div>
      )}
      {current && !active && (
        <p className="text-xs text-text-tertiary mt-2">
          The project must be active before its port can change.
        </p>
      )}
      {confirming && (
        <div className="mt-3 rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-text-primary">
          <p>
            Anyone on the internet will be able to reach this database's login. Only open it when a
            tool outside the platform needs a direct connection. Only org admins and owners can do
            this.
          </p>
          <div className="mt-2 flex gap-2">
            <Button size="sm" variant="danger" onClick={open} data-testid="public-port-confirm">
              Open public port
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
