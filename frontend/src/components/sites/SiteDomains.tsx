import { useState, type FormEvent } from 'react';
import { apiErrorMessage } from '../../api/apps';
import {
  useAddSiteDomain,
  useCheckSiteDomain,
  useRemoveSiteDomain,
  useSiteDomains,
  type DnsInstruction,
  type SiteDomain,
  type SiteDomainStatus,
  type SiteDomainValidation,
} from '../../api/siteDomains';
import { useSiteCustomDomainsEnabled } from '../../hooks/useDeploymentMode';
import { ConfirmModal } from '../ui/ConfirmModal';
import { inputClass } from '../containers/EnvVarEditor';
import { Spinner, ToneBadge, primaryButton, secondaryButton } from '../containers/ContainerBits';
import type { Tone } from '../containers/appCopy';

const STATUS: Record<SiteDomainStatus, { label: string; tone: Tone }> = {
  PENDING: { label: 'Waiting for DNS', tone: 'progress' },
  ACTIVE: { label: 'Live', tone: 'success' },
  FAILED: { label: 'Failed', tone: 'error' },
};

const PURPOSE: Record<DnsInstruction['purpose'], string> = {
  routing: 'Sends the domain to your site',
  verification: 'Proves you control the domain; nothing is served until it is seen',
  ownership: 'Proves the domain is yours to the CDN',
  certificate: 'Lets the certificate be issued',
};

function Records({ domain }: { readonly domain: SiteDomain }) {
  return (
    <table className="w-full text-xs" data-testid={`site-domain-records-${domain.id}`}>
      <thead className="text-text-tertiary text-left">
        <tr>
          <th className="font-normal pr-3">Type</th>
          <th className="font-normal pr-3">Name</th>
          <th className="font-normal pr-3">Value</th>
          <th className="font-normal">For</th>
        </tr>
      </thead>
      <tbody className="font-mono text-text-secondary">
        {domain.records.map((record) => (
          <tr key={`${record.type}-${record.name}-${record.value}`}>
            <td className="pr-3 py-0.5">{record.type}</td>
            <td className="pr-3 py-0.5 break-all">{record.name}</td>
            <td className="pr-3 py-0.5 break-all">{record.value}</td>
            <td className="py-0.5 font-sans text-text-tertiary">{PURPOSE[record.purpose]}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

interface RowProps {
  readonly domain: SiteDomain;
  readonly canDevelop: boolean;
  readonly busy: boolean;
  readonly onCheck: (id: string) => void;
  readonly onRemove: (domain: SiteDomain) => void;
}

function DomainRow({ domain, canDevelop, busy, onCheck, onRemove }: RowProps) {
  const status = STATUS[domain.status];
  const url = `https://${domain.hostname}`;
  return (
    <li className="px-4 py-3 text-sm space-y-2" data-testid={`site-domain-${domain.hostname}`}>
      <div className="flex items-center justify-between gap-3">
        {domain.status === 'ACTIVE' ? (
          <a
            href={url}
            target="_blank"
            rel="noreferrer"
            className="font-mono text-purple-400 hover:underline"
          >
            {url}
          </a>
        ) : (
          <span className="font-mono text-text-primary">{domain.hostname}</span>
        )}
        <span className="flex items-center gap-2">
          <ToneBadge
            label={status.label}
            tone={status.tone}
            testId={`site-domain-status-${domain.id}`}
          />
          {canDevelop && domain.status !== 'ACTIVE' && (
            <button
              type="button"
              onClick={() => onCheck(domain.id)}
              disabled={busy}
              className={secondaryButton}
              data-testid={`site-domain-check-${domain.id}`}
            >
              Check now
            </button>
          )}
          {canDevelop && (
            <button
              type="button"
              onClick={() => onRemove(domain)}
              disabled={busy}
              className={secondaryButton}
              data-testid={`site-domain-remove-${domain.id}`}
            >
              Remove
            </button>
          )}
        </span>
      </div>
      {domain.status !== 'ACTIVE' && (
        <>
          <p className="text-xs text-text-tertiary">
            Add these records at your DNS provider. The domain goes live once Cloudflare sees them
            and issues its certificate; this is checked every few minutes, and a domain not live
            within seven days is removed.
          </p>
          <Records domain={domain} />
        </>
      )}
      {domain.errors.map((message) => (
        <p key={message} className="text-xs text-amber-400">
          {message}
        </p>
      ))}
    </li>
  );
}

function AddDomain({ projectId, siteId }: { readonly projectId: string; readonly siteId: string }) {
  const add = useAddSiteDomain(projectId, siteId);
  const [hostname, setHostname] = useState('');
  const [validation, setValidation] = useState<SiteDomainValidation>('http');
  const submit = (event: FormEvent) => {
    event.preventDefault();
    add.mutate({ hostname: hostname.trim(), validation }, { onSuccess: () => setHostname('') });
  };
  return (
    <form onSubmit={submit} className="space-y-2">
      <div className="flex gap-2">
        <input
          aria-label="Domain"
          placeholder="www.example.com"
          value={hostname}
          onChange={(event) => setHostname(event.target.value)}
          className={inputClass}
          data-testid="site-domain-input"
        />
        <button
          type="submit"
          disabled={add.isPending || !hostname.trim()}
          className={primaryButton}
          data-testid="site-domain-add"
        >
          Add
        </button>
      </div>
      <label className="flex items-center gap-2 text-xs text-text-tertiary">
        <span>Certificate validation</span>
        <select
          value={validation}
          onChange={(event) => setValidation(event.target.value as SiteDomainValidation)}
          className="bg-bg-secondary border border-border-primary rounded px-2 py-1 text-text-primary"
          data-testid="site-domain-validation"
        >
          <option value="http">HTTP: only a CNAME to add</option>
          <option value="txt">TXT: issue the certificate before moving DNS</option>
        </select>
      </label>
      <p className="text-xs text-text-tertiary">
        A subdomain such as www.example.com, not a bare domain like example.com.
      </p>
      {add.error && (
        <div role="alert" className="text-sm text-red-400">
          {apiErrorMessage(add.error, 'The domain could not be added')}
        </div>
      )}
    </form>
  );
}

interface SiteDomainsProps {
  readonly projectId: string;
  readonly siteId: string;
  readonly canDevelop: boolean;
}

export function SiteDomains({ projectId, siteId, canDevelop }: SiteDomainsProps) {
  const { enabled, isLoading } = useSiteCustomDomainsEnabled();
  const { data: domains = [] } = useSiteDomains(projectId, siteId, enabled);
  const check = useCheckSiteDomain(projectId, siteId);
  const remove = useRemoveSiteDomain(projectId, siteId);
  const [removing, setRemoving] = useState<SiteDomain | null>(null);

  if (isLoading) return <Spinner />;
  if (!enabled) {
    return (
      <p className="text-sm text-text-tertiary" data-testid="site-domains-unavailable">
        Custom domains are not set up on this install; the site serves at its own address.
      </p>
    );
  }
  const error = check.error ?? remove.error;
  return (
    <div className="space-y-3" data-testid="site-domains">
      {domains.length > 0 && (
        <ul className="bg-surface-card border border-border-primary rounded-lg divide-y divide-border-primary">
          {domains.map((domain) => (
            <DomainRow
              key={domain.id}
              domain={domain}
              canDevelop={canDevelop}
              busy={check.isPending || remove.isPending}
              onCheck={(id) => check.mutate(id)}
              onRemove={setRemoving}
            />
          ))}
        </ul>
      )}
      {domains.length === 0 && !canDevelop && (
        <p className="text-sm text-text-tertiary">This site has no custom domain.</p>
      )}
      {canDevelop && <AddDomain projectId={projectId} siteId={siteId} />}
      {error && (
        <div role="alert" className="text-sm text-red-400">
          {apiErrorMessage(error, 'The domain could not be changed')}
        </div>
      )}
      <ConfirmModal
        open={removing !== null}
        onClose={() => setRemoving(null)}
        onConfirm={() =>
          removing ? remove.mutateAsync(removing.id).finally(() => setRemoving(null)) : undefined
        }
        title={`Remove ${removing?.hostname ?? ''}?`}
        message="The domain stops serving this site at once and its certificate is revoked. Remove its DNS records afterwards."
        confirmLabel="Remove domain"
        destructive
        loading={remove.isPending}
      />
    </div>
  );
}
