import { Loader2, Network } from 'lucide-react';
import { useFunctionEgress, useSetFunctionEgress } from '../api/functionEgress';
import { serverErrorMessage } from '../utils/serverError';
import { AllowList } from './AllowList';

// FunctionEgressCard edits the outside hosts the project's edge functions may
// call. Saving replaces the list, and the server redeploys the functions.
export function FunctionEgressCard({ projectId }: { readonly projectId: string }) {
  const egress = useFunctionEgress(projectId);
  const save = useSetFunctionEgress(projectId);
  const hosts = egress.data?.allowedHosts ?? [];
  const defaults = egress.data?.defaultHosts ?? [];

  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4" data-testid="egress-card">
      <h4 className="flex items-center gap-2 text-sm font-medium text-text-primary mb-2">
        <Network className="w-4 h-4" /> Outbound hosts
      </h4>
      <p className="text-xs text-text-secondary mb-3">
        Functions can call only these outside hosts; anything else is blocked. Enter <code>host</code> or{' '}
        <code>host:port</code>: without a port, <code>:443</code> (HTTPS) is used. <code>*.example.com</code>{' '}
        allows the subdomains of example.com. Only public hosts are accepted. Saving redeploys your functions.
      </p>

      {egress.isLoading && <Loader2 className="w-4 h-4 animate-spin" />}
      {egress.isError && (
        <p className="text-xs text-red-400" data-testid="egress-error">
          {serverErrorMessage(egress.error, 'The outbound hosts could not be read')}
        </p>
      )}
      {egress.data && (
        <>
          <AllowList
            name="egress-hosts"
            entries={hosts}
            emptyText="No outside host is allowed: every outbound call is blocked."
            inputLabel="Host"
            placeholder="api.example.com"
            addLabel="Add host"
            busy={save.isPending}
            error={save.error}
            onAdd={(host, done) => save.mutate([...hosts, host], { onSuccess: done })}
            onRemove={(host) => save.mutate(hosts.filter((item) => item !== host))}
          />
          {defaults.length > 0 && (
            <p className="text-xs text-text-tertiary mt-3" data-testid="egress-default-hosts">
              Always allowed on this installation: {defaults.join(', ')}
            </p>
          )}
        </>
      )}
    </div>
  );
}
