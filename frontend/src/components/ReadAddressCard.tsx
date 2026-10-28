import { BookOpen, Loader2 } from 'lucide-react';
import { useReadAddress, useSetReadAddress, type ReadAddress } from '../api/readAddress';
import { refusalMessage } from '../api/clusterSettings';
import { useFeatureEnabled } from '../hooks/useDeploymentMode';
import { Button } from './Button';

interface ReadAddressCardProps {
  readonly projectId: string;
  readonly status: string;
}

const LAG_NOTE =
  'Replicas apply each change a moment after the primary commits it, so a read here can miss a write made just before. ' +
  'Read your own recent writes from the main address. Every write is refused on this address, whoever signs in.';

const NO_REPLICA =
  'This database runs one copy, so there is no replica to read from. A plan with more than one copy gets a read address.';

const SINGLE_HOST =
  'A single host runs one copy of the database, so there is no replica to read from. ' +
  'Read replicas need a Kubernetes cluster with a node per copy.';

// ReadAddressCard shows the database's read-only address to its replicas and
// opens or closes its public port (EXC-666). Hidden while the flag is off.
export function ReadAddressCard({ projectId, status }: ReadAddressCardProps) {
  const feature = useFeatureEnabled('dbreadaddress');
  const readAddress = useReadAddress(projectId, feature.enabled);
  if (!feature.enabled) return null;

  return (
    <div
      className="rounded-lg border border-border-primary bg-surface-card p-4"
      data-testid="read-address-card"
    >
      <h4 className="flex items-center gap-2 text-sm font-medium text-text-primary mb-2">
        <BookOpen className="w-4 h-4" /> Read-only address
      </h4>
      {readAddress.isLoading && <Loader2 className="w-4 h-4 animate-spin" />}
      {readAddress.isError && (
        <p className="text-xs text-text-tertiary" data-testid="read-address-error">
          The read address could not be read: {refusalMessage(readAddress.error)}
        </p>
      )}
      {readAddress.data && (
        <ReadAddressBody projectId={projectId} status={status} view={readAddress.data} />
      )}
    </div>
  );
}

function ReadAddressBody({
  projectId,
  status,
  view,
}: {
  readonly projectId: string;
  readonly status: string;
  readonly view: ReadAddress;
}) {
  if (view.reason === 'single-host' || view.singleHost) {
    return (
      <p className="text-xs text-text-secondary" data-testid="read-address-none">
        {SINGLE_HOST}
      </p>
    );
  }
  if (view.reason === 'one-copy' && !view.enabled) {
    return (
      <p className="text-xs text-text-secondary" data-testid="read-address-none">
        {NO_REPLICA}
      </p>
    );
  }
  return (
    <>
      <p className="text-xs text-text-secondary mb-2" data-testid="read-address-replicas">
        {view.replicas === 0
          ? 'No replica is serving right now, so this address refuses connections.'
          : `${view.replicas} ${view.replicas === 1 ? 'replica serves' : 'replicas serve'} reads.`}
      </p>
      {view.internal && (
        <p className="text-sm text-text-primary mb-2" data-testid="read-address-internal">
          Inside the project:{' '}
          <code className="font-mono text-xs">
            {view.internal.host}:{view.internal.port}
          </code>
        </p>
      )}
      <PublicReadPort projectId={projectId} status={status} view={view} />
      <p className="text-xs text-text-tertiary mt-3" data-testid="read-address-lag">
        {LAG_NOTE}
      </p>
    </>
  );
}

function PublicReadPort({
  projectId,
  status,
  view,
}: {
  readonly projectId: string;
  readonly status: string;
  readonly view: ReadAddress;
}) {
  const change = useSetReadAddress(projectId);
  if (!view.publicOffered) {
    return (
      <p className="text-xs text-text-tertiary">
        This installation does not offer public database ports.
      </p>
    );
  }
  const active = status === 'ACTIVE';
  const canOpen = view.publicOpen && view.replicas > 0;
  return (
    <div>
      <div className="flex items-center justify-between gap-4">
        <p className="text-sm text-text-primary" data-testid="read-address-public">
          {view.enabled ? (
            <>
              Public read port at{' '}
              <code className="font-mono text-xs">
                {view.host}:{view.port}
              </code>
              {!view.available && (
                <span className="text-xs text-text-tertiary"> (not answering right now)</span>
              )}
            </>
          ) : (
            'No public read port'
          )}
        </p>
        {view.canChange && (view.enabled || canOpen) && (
          <Button
            size="sm"
            variant={view.enabled ? 'secondary' : 'primary'}
            disabled={!active || change.isPending}
            onClick={() => change.mutate(!view.enabled)}
            data-testid="read-address-toggle"
          >
            {view.enabled ? 'Close read port' : 'Open read port'}
          </Button>
        )}
      </div>
      {!view.enabled && !view.publicOpen && (
        <p className="text-xs text-text-tertiary mt-2" data-testid="read-address-needs-public">
          Open the public database port first; the read port sits beside it.
        </p>
      )}
      {!view.canChange && (
        <p className="text-xs text-text-tertiary mt-2" data-testid="read-address-admin-only">
          Only an org admin or owner can open or close the read port.
        </p>
      )}
      {change.isError && (
        <p role="alert" className="text-xs text-color-error mt-2">
          {refusalMessage(change.error)}
        </p>
      )}
    </div>
  );
}
