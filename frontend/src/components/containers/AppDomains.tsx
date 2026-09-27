import { useState, type FormEvent } from 'react';
import { Globe } from 'lucide-react';
import { apiErrorMessage } from '../../api/apps';
import {
  useAddAppDomain,
  useAppDomains,
  useRemoveAppDomain,
  useVerifyAppDomain,
  type AppDomain,
  type DomainStatus,
} from '../../api/appDomains';
import { inputClass } from './EnvVarEditor';
import { ToneBadge, primaryButton, secondaryButton } from './ContainerBits';
import type { Tone } from './appCopy';

const STATUS: Record<DomainStatus, { label: string; tone: Tone }> = {
  pending: { label: 'Waiting for DNS', tone: 'neutral' },
  issuing: { label: 'Issuing certificate', tone: 'progress' },
  active: { label: 'Live', tone: 'success' },
  issue_failed: { label: 'Certificate failed', tone: 'error' },
  detached: { label: 'Detached', tone: 'error' },
};

function DomainRow({
  domain,
  onVerify,
  onRemove,
  busy,
}: {
  readonly domain: AppDomain;
  readonly onVerify: (id: string) => void;
  readonly onRemove: (id: string) => void;
  readonly busy: boolean;
}) {
  const status = STATUS[domain.status];
  return (
    <li className="px-4 py-3 text-sm space-y-1" data-testid={`domain-row-${domain.hostname}`}>
      <div className="flex items-center justify-between gap-3">
        <span className="font-mono text-text-primary">{domain.hostname}</span>
        <span className="flex items-center gap-2">
          <ToneBadge label={status.label} tone={status.tone} />
          {(domain.status === 'pending' || domain.status === 'detached') && (
            <button
              type="button"
              onClick={() => onVerify(domain.id)}
              disabled={busy}
              className={secondaryButton}
              data-testid={`domain-verify-${domain.id}`}
            >
              Verify
            </button>
          )}
          <button
            type="button"
            onClick={() => onRemove(domain.id)}
            disabled={busy}
            className={secondaryButton}
            data-testid={`domain-remove-${domain.id}`}
          >
            Remove
          </button>
        </span>
      </div>
      {domain.status === 'pending' && (
        <p className="text-xs text-text-tertiary">
          Add a CNAME record for {domain.hostname} pointing at{' '}
          <span className="font-mono text-text-secondary">{domain.cnameTarget}</span>, then verify.
        </p>
      )}
      {domain.failureReason && <p className="text-xs text-red-400">{domain.failureReason}</p>}
    </li>
  );
}

export function AppDomains({
  projectId,
  appId,
}: {
  readonly projectId: string;
  readonly appId: string;
}) {
  const { data: domains = [] } = useAppDomains(projectId, appId);
  const add = useAddAppDomain(projectId, appId);
  const verify = useVerifyAppDomain(projectId, appId);
  const remove = useRemoveAppDomain(projectId, appId);
  const [hostname, setHostname] = useState('');
  const busy = add.isPending || verify.isPending || remove.isPending;
  const error = add.error ?? verify.error ?? remove.error;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    add.mutate(hostname.trim(), { onSuccess: () => setHostname('') });
  };

  return (
    <section className="space-y-2" data-testid="app-domains">
      <h4 className="text-sm font-semibold text-text-primary flex items-center gap-2">
        <Globe className="w-4 h-4" /> Custom domains
      </h4>
      {domains.length > 0 && (
        <ul className="bg-surface-card border border-border-primary rounded-lg divide-y divide-border-primary">
          {domains.map((domain) => (
            <DomainRow
              key={domain.id}
              domain={domain}
              busy={busy}
              onVerify={(id) => verify.mutate(id)}
              onRemove={(id) => remove.mutate(id)}
            />
          ))}
        </ul>
      )}
      <form onSubmit={submit} className="flex gap-2">
        <input
          aria-label="Domain"
          placeholder="www.example.com"
          value={hostname}
          onChange={(e) => setHostname(e.target.value)}
          className={inputClass}
          data-testid="domain-input"
        />
        <button
          type="submit"
          disabled={busy || !hostname.trim()}
          className={primaryButton}
          data-testid="domain-add"
        >
          Add
        </button>
      </form>
      {error && (
        <div role="alert" className="text-sm text-red-400">
          {apiErrorMessage(error, 'The domain could not be changed')}
        </div>
      )}
    </section>
  );
}
