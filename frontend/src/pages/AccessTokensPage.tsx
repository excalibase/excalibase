import { useState } from 'react';
import { AlertTriangle, Check, Copy, KeyRound, Loader2 } from 'lucide-react';
import { Button } from '../components/Button';
import { ConfirmModal } from '../components/ui/ConfirmModal';
import { useInstances } from '../hooks/useProvisioning';
import {
  scopeLabel,
  useAccessTokens,
  useCreateAccessToken,
  useRevokeAccessToken,
  type AccessToken,
  type CreatedAccessToken,
  type TokenAccess,
} from '../api/accessTokens';

const EXPIRY_OPTIONS = [
  { value: '7d', label: '7 days' },
  { value: '30d', label: '30 days' },
  { value: '90d', label: '90 days' },
  { value: '365d', label: '1 year' },
  { value: 'never', label: 'No expiry' },
];

const FIELD = 'px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary';

function errorText(err: unknown, fallback: string): string {
  const axiosErr = err as { response?: { data?: { error?: string } } };
  return axiosErr?.response?.data?.error || fallback;
}

function formatDate(value?: string | null): string {
  return value ? new Date(value).toLocaleString() : 'Never';
}

function expiryText(value?: string | null): string {
  if (!value) return 'Never';
  return new Date(value).getTime() < Date.now() ? `Expired ${formatDate(value)}` : formatDate(value);
}

type CopyState = 'idle' | 'copied' | 'failed';

function NewTokenNotice({ created, onDone }: { readonly created: CreatedAccessToken; readonly onDone: () => void }) {
  const [copy, setCopy] = useState<CopyState>('idle');
  const copyToken = async () => {
    try {
      await navigator.clipboard.writeText(created.token);
      setCopy('copied');
    } catch {
      setCopy('failed');
    }
  };
  return (
    <div data-testid="new-access-token" className="space-y-3 p-4 rounded-lg border border-purple-500/30 bg-purple-500/10">
      <p className="text-sm text-text-primary">
        Copy <strong>{created.name}</strong> now. It will not be shown again.
      </p>
      <div className="flex items-center gap-2">
        <code className="flex-1 px-3 py-2 rounded bg-bg-secondary text-text-primary text-xs break-all">{created.token}</code>
        <button type="button" onClick={copyToken} aria-label="Copy token" className="p-2 text-text-secondary hover:text-text-primary">
          {copy === 'copied' ? <Check className="w-4 h-4" /> : <Copy className="w-4 h-4" />}
        </button>
      </div>
      {copy === 'copied' && <p className="text-xs text-text-secondary">Copied to the clipboard.</p>}
      {copy === 'failed' && <p className="text-xs text-red-400">The browser refused the clipboard; select the token and copy it by hand.</p>}
      <Button type="button" onClick={onDone}>I have saved it</Button>
    </div>
  );
}

function CreateTokenForm({ onCreated, onError }: {
  readonly onCreated: (token: CreatedAccessToken) => void;
  readonly onError: (message: string | null) => void;
}) {
  const createToken = useCreateAccessToken();
  const projects = useInstances();
  const [name, setName] = useState('');
  const [access, setAccess] = useState<TokenAccess>('read');
  const [projectId, setProjectId] = useState('');
  const [expiresIn, setExpiresIn] = useState('90d');

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      onError('Give the token a name so you can recognise it later.');
      return;
    }
    onError(null);
    try {
      const token = await createToken.mutateAsync({ name: trimmed, access, expiresIn, projectId });
      // Only the notice keeps the secret; the mutation forgets it.
      createToken.reset();
      onCreated(token);
      setName('');
    } catch (err) {
      onError(errorText(err, 'Could not create the token'));
    }
  };

  return (
    <form onSubmit={submit} className="space-y-3">
      <div className="flex flex-wrap items-end gap-3">
        <div>
          <label htmlFor="token-name" className="block text-sm text-text-secondary mb-1">Token name</label>
          <input id="token-name" value={name} maxLength={100} onChange={(e) => setName(e.target.value)}
            className={FIELD} placeholder="storefront seed" />
        </div>
        <div>
          <label htmlFor="token-access" className="block text-sm text-text-secondary mb-1">Access</label>
          <select id="token-access" value={access} onChange={(e) => setAccess(e.target.value as TokenAccess)} className={FIELD}>
            <option value="read">Read only</option>
            <option value="write">Read and write</option>
          </select>
        </div>
        <div>
          <label htmlFor="token-project" className="block text-sm text-text-secondary mb-1">Project</label>
          <select id="token-project" value={projectId} onChange={(e) => setProjectId(e.target.value)} className={FIELD}>
            <option value="">All your projects</option>
            {(projects.data ?? []).map((p) => (
              <option key={p.projectId} value={p.projectId}>{p.projectName || p.projectId}</option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="token-expires" className="block text-sm text-text-secondary mb-1">Expires</label>
          <select id="token-expires" value={expiresIn} onChange={(e) => setExpiresIn(e.target.value)} className={FIELD}>
            {EXPIRY_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </div>
        <Button type="submit" disabled={createToken.isPending} className="flex items-center gap-2">
          {createToken.isPending && <Loader2 className="w-4 h-4 animate-spin" />}
          Create token
        </Button>
      </div>
      {expiresIn === 'never' && (
        <p className="flex items-center gap-2 text-sm text-amber-300">
          <AlertTriangle className="w-4 h-4" />
          This token never expires. Keep it for automation you rotate yourself, and revoke it when it is no longer used.
        </p>
      )}
    </form>
  );
}

function TokenTable({ tokens, projectName, onRevoke }: {
  readonly tokens: AccessToken[];
  readonly projectName: (id: string) => string;
  readonly onRevoke: (token: AccessToken) => void;
}) {
  return (
    <table className="w-full text-sm">
      <thead className="text-left text-text-secondary">
        <tr><th className="py-2">Name</th><th>Access</th><th>Project</th><th>Created</th><th>Last used</th><th>Expires</th><th /></tr>
      </thead>
      <tbody>
        {tokens.map((token) => (
          <tr key={token.id} data-testid={`access-token-${token.id}`} className="border-t border-border-primary text-text-primary">
            <td className="py-2">
              <div>{token.name}</div>
              <code className="text-xs text-text-tertiary">{token.tokenPrefix}…</code>
            </td>
            <td>{scopeLabel(token.scopes)}</td>
            <td>{token.projectId ? projectName(token.projectId) : 'All your projects'}</td>
            <td data-testid="token-created">{formatDate(token.createdAt)}</td>
            <td data-testid="token-last-used">{formatDate(token.lastUsed)}</td>
            <td data-testid="token-expires">{expiryText(token.expiresAt)}</td>
            <td className="text-right">
              <button type="button" onClick={() => onRevoke(token)} className="text-red-400 hover:text-red-300">Revoke</button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function AccessTokensPage() {
  const tokens = useAccessTokens();
  const projects = useInstances();
  const revokeToken = useRevokeAccessToken();
  const [created, setCreated] = useState<CreatedAccessToken | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<AccessToken | null>(null);

  const projectName = (id: string) => projects.data?.find((p) => p.projectId === id)?.projectName || id;

  const confirmRevoke = async () => {
    if (!revoking) return;
    setError(null);
    try {
      await revokeToken.mutateAsync(revoking.id);
    } catch (err) {
      setError(errorText(err, 'Could not revoke the token'));
    } finally {
      setRevoking(null);
    }
  };

  return (
    <div className="space-y-6 max-w-5xl">
      <div>
        <p className="text-xs uppercase tracking-wide text-text-tertiary">Account settings</p>
        <h1 className="flex items-center gap-2 text-xl font-semibold text-text-primary"><KeyRound className="w-5 h-5" /> Access tokens</h1>
        <p className="text-sm text-text-secondary mt-1">
          Personal access tokens let scripts and CI call the Excalibase API as you. A token never does more than your
          own account: every request is checked against your role in each organization. A password reset revokes every
          access token.
        </p>
      </div>

      {error && <div className="px-4 py-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">{error}</div>}
      {created && <NewTokenNotice key={created.prefix} created={created} onDone={() => setCreated(null)} />}

      <CreateTokenForm onCreated={setCreated} onError={setError} />

      {tokens.isLoading && <Loader2 className="w-5 h-5 animate-spin text-text-secondary" />}
      {tokens.isError && <p className="text-sm text-red-400">{errorText(tokens.error, 'Could not load your tokens')}</p>}
      {tokens.data?.length === 0 && <p className="text-sm text-text-secondary">No access tokens yet.</p>}
      {!!tokens.data?.length && <TokenTable tokens={tokens.data} projectName={projectName} onRevoke={setRevoking} />}

      <ConfirmModal
        open={revoking !== null}
        onClose={() => setRevoking(null)}
        onConfirm={() => { void confirmRevoke(); }}
        title="Revoke access token"
        message={`Revoke "${revoking?.name ?? ''}"? Anything using it stops working immediately. This cannot be undone.`}
        confirmLabel="Revoke"
        destructive
        loading={revokeToken.isPending}
      />
    </div>
  );
}
