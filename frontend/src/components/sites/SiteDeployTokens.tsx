import { useState } from 'react';
import { Copy, KeyRound, Trash2 } from 'lucide-react';
import { apiErrorMessage } from '../../api/apps';
import {
  DEFAULT_DEPLOY_TOKEN_DAYS,
  DEPLOY_TOKEN_EXPIRY_DAYS,
  useCreateDeployToken,
  useDeployTokens,
  useRevokeDeployToken,
  type DeployToken,
  type IssuedDeployToken,
} from '../../api/sites';
import { ConfirmModal } from '../ui/ConfirmModal';
import { formatWhen } from '../containers/appCopy';
import { Spinner, primaryButton, secondaryButton } from '../containers/ContainerBits';

interface SiteRef {
  readonly projectId: string;
  readonly siteId: string;
}

// The raw token, shown once right after it is made; the server keeps only its hash.
function IssuedToken({
  issued,
  onDone,
}: {
  readonly issued: IssuedDeployToken;
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
      data-testid="site-deploy-token-issued"
    >
      <p className="text-sm text-text-primary">
        Copy {issued.name} into your CI as the secret EXCALIBASE_DEPLOY_TOKEN now: it is shown once.
        It can only deploy this site and read its deploys.
      </p>
      <code className="block text-xs font-mono break-all bg-bg-tertiary border border-border-primary rounded-lg p-2">
        {issued.token}
      </code>
      <div className="flex gap-2">
        <button type="button" onClick={copy} className={secondaryButton}>
          <Copy className="w-3.5 h-3.5" /> {copied ? 'Copied' : 'Copy'}
        </button>
        <button type="button" onClick={onDone} className={secondaryButton}>
          Done
        </button>
      </div>
    </div>
  );
}

// tokenStatus is why a token would be refused now, or how long it has left.
function tokenStatus(token: DeployToken): { readonly text: string; readonly refused: boolean } {
  if (new Date(token.expiresAt).getTime() <= Date.now()) {
    return { text: `Expired ${formatWhen(token.expiresAt)}`, refused: true };
  }
  if (token.creatorCanDeploy === false) {
    return { text: 'inactive (creator no longer a Developer)', refused: true };
  }
  return { text: `Expires ${formatWhen(token.expiresAt)}`, refused: false };
}

function TokenRow({
  token,
  onRevoke,
}: {
  readonly token: DeployToken;
  readonly onRevoke: () => void;
}) {
  const status = tokenStatus(token);
  return (
    <li
      className="flex items-center justify-between gap-3 px-3 py-2"
      data-testid={`site-deploy-token-${token.id}`}
    >
      <div className="min-w-0">
        <p className="text-sm text-text-primary truncate">{token.name}</p>
        <p className="text-xs text-text-tertiary">
          <span className="font-mono">{token.tokenPrefix}…</span> · made{' '}
          {formatWhen(token.createdAt)} ·{' '}
          {token.lastUsedAt ? `Last used ${formatWhen(token.lastUsedAt)}` : 'Never used'}
        </p>
        <p className={`text-xs ${status.refused ? 'text-amber-400' : 'text-text-tertiary'}`}>
          {status.text}
        </p>
      </div>
      <button
        type="button"
        onClick={onRevoke}
        className={secondaryButton}
        data-testid={`site-deploy-token-revoke-${token.id}`}
      >
        <Trash2 className="w-3.5 h-3.5" /> Revoke
      </button>
    </li>
  );
}

function CreateToken({
  projectId,
  siteId,
  onIssued,
}: SiteRef & { readonly onIssued: (issued: IssuedDeployToken) => void }) {
  const create = useCreateDeployToken(projectId, siteId);
  const [name, setName] = useState('');
  const [expiresInDays, setExpiresInDays] = useState<number>(DEFAULT_DEPLOY_TOKEN_DAYS);
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    create.mutate(
      { name: name.trim(), expiresInDays },
      {
        onSuccess: (issued) => {
          setName('');
          onIssued(issued);
          // The raw token lives only in the panel that shows it once, not in the mutation cache.
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
          placeholder="Token name, such as github actions"
          aria-label="Deploy token name"
          className="flex-1 min-w-0 px-3 py-1.5 text-sm rounded-lg bg-bg-tertiary border border-border-primary text-text-primary focus:outline-none focus:border-purple-500/50"
          data-testid="site-deploy-token-name"
        />
        <select
          value={expiresInDays}
          onChange={(event) => setExpiresInDays(Number(event.target.value))}
          aria-label="Deploy token expires after"
          className="px-2 py-1.5 text-sm rounded-lg bg-bg-tertiary border border-border-primary text-text-primary focus:outline-none focus:border-purple-500/50"
          data-testid="site-deploy-token-expiry"
        >
          {DEPLOY_TOKEN_EXPIRY_DAYS.map((days) => (
            <option key={days} value={days}>
              {days === 365 ? '1 year' : `${days} days`}
            </option>
          ))}
        </select>
        <button
          type="submit"
          disabled={!name.trim() || create.isPending}
          className={primaryButton}
          data-testid="site-deploy-token-create"
        >
          <KeyRound className="w-4 h-4" /> Create token
        </button>
      </div>
      {create.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(create.error, 'The token could not be made')}
        </p>
      )}
    </form>
  );
}

// Deploy tokens (EXC-620): CI credentials that can only deploy this site.
// Made and revoked by a Developer or above; revoking stops the next request.
export function SiteDeployTokens({ projectId, siteId }: SiteRef) {
  const { data: tokens, error } = useDeployTokens(projectId, siteId);
  const revoke = useRevokeDeployToken(projectId, siteId);
  const [issued, setIssued] = useState<IssuedDeployToken | null>(null);
  const [revoking, setRevoking] = useState<DeployToken | null>(null);

  return (
    <section
      className="bg-surface-card border border-border-primary rounded-lg p-4 space-y-3"
      data-testid="site-deploy-tokens"
    >
      <p className="text-xs text-text-tertiary">
        A deploy token can deploy this site and read its deploys, nothing else. It acts for the
        Developer who made it, stops working if they leave the project, and expires when chosen
        (never later than the access token that made it).
      </p>
      {issued && <IssuedToken issued={issued} onDone={() => setIssued(null)} />}
      <CreateToken projectId={projectId} siteId={siteId} onIssued={setIssued} />
      {error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(error, 'Could not load the deploy tokens')}
        </p>
      )}
      {!tokens && !error && <Spinner />}
      {tokens?.length === 0 && (
        <p className="text-sm text-text-secondary" data-testid="site-deploy-tokens-empty">
          No deploy tokens yet. Make one for each CI that deploys this site.
        </p>
      )}
      {tokens && tokens.length > 0 && (
        <ul className="divide-y divide-border-primary border border-border-primary rounded-lg">
          {tokens.map((token) => (
            <TokenRow key={token.id} token={token} onRevoke={() => setRevoking(token)} />
          ))}
        </ul>
      )}
      {revoke.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(revoke.error, 'The token could not be revoked')}
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
        title={`Revoke ${revoking?.name ?? 'this token'}?`}
        message="Any CI using it is refused from its next request. This cannot be undone."
        confirmLabel="Revoke token"
        destructive
        loading={revoke.isPending}
      />
    </section>
  );
}
