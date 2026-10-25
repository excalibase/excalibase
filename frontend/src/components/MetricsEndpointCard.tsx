import { useState } from 'react';
import { Copy, KeyRound, Trash2 } from 'lucide-react';
import { apiErrorMessage } from '../api/apps';
import { API_BASE } from '../api/base';
import {
  DEFAULT_METRICS_KEY_DAYS,
  METRICS_KEY_EXPIRY_DAYS,
  useCreateMetricsKey,
  useMetricsKeys,
  useRevokeMetricsKey,
  type IssuedMetricsKey,
  type MetricsKey,
} from '../api/metricsKeys';
import { useFeatureEnabled } from '../hooks/useDeploymentMode';
import { useProjectRole } from '../hooks/useProjectRole';
import { ConfirmModal } from './ui/ConfirmModal';
import { formatWhen } from './containers/appCopy';
import { Spinner, primaryButton, secondaryButton } from './containers/ContainerBits';

interface ProjectRef {
  readonly projectId: string;
}

// The endpoint as the scraper reaches it: the API base resolved against Studio's own origin.
export function metricsEndpointUrl(projectId: string): string {
  const base = new URL(API_BASE, globalThis.location.origin).toString().replace(/\/$/, '');
  return `${base}/projects/${projectId}/metrics`;
}

function scrapeConfig(url: string): string {
  const endpoint = new URL(url);
  return [
    'scrape_configs:',
    '  - job_name: excalibase',
    `    scheme: ${endpoint.protocol.replace(':', '')}`,
    `    metrics_path: ${endpoint.pathname}`,
    '    scrape_interval: 60s',
    '    authorization:',
    '      type: Bearer',
    '      credentials_file: /etc/prometheus/excalibase-metrics-key',
    '    static_configs:',
    `      - targets: ['${endpoint.host}']`,
  ].join('\n');
}

// The raw key, shown once right after it is made; the server keeps only its hash.
function IssuedKey({
  issued,
  url,
  onDone,
}: {
  readonly issued: IssuedMetricsKey;
  readonly url: string;
  readonly onDone: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard.writeText(issued.token);
    setCopied(true);
  };
  return (
    <div
      className="rounded-lg border border-purple-500/30 bg-purple-500/5 p-3 space-y-2"
      data-testid="metrics-key-issued"
    >
      <p className="text-sm text-text-primary">
        Copy {issued.name} now: it is shown once. Save it in a file your scraper reads and send it
        only as a Bearer header. It can read this project&apos;s metrics and nothing else.
      </p>
      <code className="block text-xs font-mono break-all bg-bg-tertiary border border-border-primary rounded-lg p-2">
        {issued.token}
      </code>
      <pre className="text-xs font-mono bg-bg-tertiary border border-border-primary rounded-lg p-2 overflow-x-auto">
        {scrapeConfig(url)}
      </pre>
      <div className="flex gap-2">
        <button type="button" onClick={copy} className={secondaryButton}>
          <Copy className="w-3.5 h-3.5" /> {copied ? 'Copied' : 'Copy key'}
        </button>
        <button type="button" onClick={onDone} className={secondaryButton}>
          Done
        </button>
      </div>
    </div>
  );
}

function keyStatus(key: MetricsKey): { readonly text: string; readonly refused: boolean } {
  if (key.expiresAt && new Date(key.expiresAt).getTime() <= Date.now()) {
    return { text: `Expired ${formatWhen(key.expiresAt)}`, refused: true };
  }
  return { text: key.expiresAt ? `Expires ${formatWhen(key.expiresAt)}` : '', refused: false };
}

function KeyRow({
  metricsKey,
  onRevoke,
}: {
  readonly metricsKey: MetricsKey;
  readonly onRevoke: () => void;
}) {
  const status = keyStatus(metricsKey);
  return (
    <li
      className="flex items-center justify-between gap-3 px-3 py-2"
      data-testid={`metrics-key-${metricsKey.id}`}
    >
      <div className="min-w-0">
        <p className="text-sm text-text-primary truncate">{metricsKey.name}</p>
        <p className="text-xs text-text-tertiary">
          <span className="font-mono">{metricsKey.tokenPrefix}…</span> · made{' '}
          {formatWhen(metricsKey.createdAt)} ·{' '}
          {metricsKey.lastUsed ? `Last used ${formatWhen(metricsKey.lastUsed)}` : 'Never used'}
        </p>
        <p className={`text-xs ${status.refused ? 'text-amber-400' : 'text-text-tertiary'}`}>
          {status.text}
        </p>
      </div>
      <button
        type="button"
        onClick={onRevoke}
        className={secondaryButton}
        data-testid={`metrics-key-revoke-${metricsKey.id}`}
      >
        <Trash2 className="w-3.5 h-3.5" /> Revoke
      </button>
    </li>
  );
}

function CreateKey({
  projectId,
  onIssued,
}: ProjectRef & { readonly onIssued: (issued: IssuedMetricsKey) => void }) {
  const create = useCreateMetricsKey(projectId);
  const [name, setName] = useState('');
  const [expiresInDays, setExpiresInDays] = useState<number>(DEFAULT_METRICS_KEY_DAYS);
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    create.mutate(
      { name: name.trim(), expiresInDays },
      {
        onSuccess: (issued) => {
          setName('');
          onIssued(issued);
          // The raw key lives only in the panel that shows it once, not in the mutation cache.
          create.reset();
        },
      },
    );
  };
  return (
    <form onSubmit={submit} className="space-y-2">
      <div className="flex gap-2">
        <input
          type="text"
          value={name}
          maxLength={64}
          onChange={(event) => setName(event.target.value)}
          placeholder="Key name, such as grafana"
          aria-label="Metrics key name"
          className="flex-1 min-w-0 px-3 py-1.5 text-sm rounded-lg bg-bg-tertiary border border-border-primary text-text-primary focus:outline-none focus:border-purple-500/50"
          data-testid="metrics-key-name"
        />
        <select
          value={expiresInDays}
          onChange={(event) => setExpiresInDays(Number(event.target.value))}
          aria-label="Metrics key expires after"
          className="px-2 py-1.5 text-sm rounded-lg bg-bg-tertiary border border-border-primary text-text-primary focus:outline-none focus:border-purple-500/50"
          data-testid="metrics-key-expiry"
        >
          {METRICS_KEY_EXPIRY_DAYS.map((days) => (
            <option key={days} value={days}>
              {days === 365 ? '1 year' : `${days} days`}
            </option>
          ))}
        </select>
        <button
          type="submit"
          disabled={!name.trim() || create.isPending}
          className={primaryButton}
          data-testid="metrics-key-create"
        >
          <KeyRound className="w-4 h-4" /> Create key
        </button>
      </div>
      {create.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(create.error, 'The key could not be made')}
        </p>
      )}
    </form>
  );
}

function MetricsKeys({ projectId }: ProjectRef) {
  const { data: keys, error } = useMetricsKeys(projectId, true);
  const revoke = useRevokeMetricsKey(projectId);
  const [issued, setIssued] = useState<IssuedMetricsKey | null>(null);
  const [revoking, setRevoking] = useState<MetricsKey | null>(null);
  const url = metricsEndpointUrl(projectId);

  return (
    <section
      className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-3"
      data-testid="metrics-endpoint"
    >
      <h3 className="text-sm font-semibold text-text-primary">Prometheus endpoint</h3>
      <p className="text-xs text-text-tertiary">
        Grafana, Prometheus or Datadog can scrape this project&apos;s metrics from{' '}
        <code className="font-mono text-text-secondary break-all">{url}</code> with a metrics key. A
        key reads only this endpoint, acts for the Admin who made it and stops working if they leave
        the project.
      </p>
      {issued && <IssuedKey issued={issued} url={url} onDone={() => setIssued(null)} />}
      <CreateKey projectId={projectId} onIssued={setIssued} />
      {error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(error, 'Could not load the metrics keys')}
        </p>
      )}
      {!keys && !error && <Spinner />}
      {keys?.length === 0 && <p className="text-sm text-text-secondary">No metrics keys yet.</p>}
      {keys && keys.length > 0 && (
        <ul className="divide-y divide-border-primary border border-border-primary rounded-lg">
          {keys.map((key) => (
            <KeyRow key={key.id} metricsKey={key} onRevoke={() => setRevoking(key)} />
          ))}
        </ul>
      )}
      {revoke.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(revoke.error, 'The key could not be revoked')}
        </p>
      )}
      <ConfirmModal
        open={revoking !== null}
        onClose={() => setRevoking(null)}
        onConfirm={() =>
          revoking
            ? revoke.mutateAsync(revoking.id).then(
                () => setRevoking(null),
                () => setRevoking(null),
              )
            : undefined
        }
        title={`Revoke ${revoking?.name ?? 'this key'}?`}
        message="Any scraper using it is refused from its next request. This cannot be undone."
        confirmLabel="Revoke key"
        destructive
        loading={revoke.isPending}
      />
    </section>
  );
}

function AdminsOnly({ projectId }: ProjectRef) {
  const { canAdmin } = useProjectRole(projectId);
  return canAdmin ? <MetricsKeys projectId={projectId} /> : null;
}

// Shown to project Admins while the metricsendpoint feature is on (EXC-657);
// while it is off nothing beyond the Studio config is read.
export function MetricsEndpointCard({ projectId }: ProjectRef) {
  const { enabled } = useFeatureEnabled('metricsendpoint');
  return enabled ? <AdminsOnly projectId={projectId} /> : null;
}
