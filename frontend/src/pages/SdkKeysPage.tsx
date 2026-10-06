import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { AlertTriangle, Check, Copy, KeyRound, Loader2 } from 'lucide-react';
import { Button } from '../components/Button';
import { ConnectSnippets } from '../components/ConnectSnippets';
import {
  displayPrefix,
  useCreateSdkKey,
  useRevokeSdkKey,
  useSdkKeys,
  type CreatedSdkKey,
  type SdkKeyType,
} from '../api/sdkKeys';

function errorText(err: unknown, fallback: string): string {
  const axiosErr = err as { response?: { data?: { error?: string } } };
  return axiosErr?.response?.data?.error || fallback;
}

function NewKeyNotice({ created, onDone }: { readonly created: CreatedSdkKey; readonly onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard?.writeText(created.plaintext);
    setCopied(true);
  };
  return (
    <div data-testid="new-sdk-key" className="space-y-3 p-4 rounded-lg border border-purple-500/30 bg-purple-500/10">
      <p className="text-sm text-text-primary">Copy this key now. It will not be shown again.</p>
      {created.keyType === 'secret' && (
        <p className="flex items-center gap-2 text-sm text-amber-300">
          <AlertTriangle className="w-4 h-4" />
          Server only: keep this key on your own server. Never ship it in browser or mobile code.
        </p>
      )}
      <div className="flex items-center gap-2">
        <code className="flex-1 px-3 py-2 rounded bg-bg-secondary text-text-primary text-xs break-all">{created.plaintext}</code>
        <button type="button" onClick={copy} aria-label="Copy key" className="p-2 text-text-secondary hover:text-text-primary">
          {copied ? <Check className="w-4 h-4" /> : <Copy className="w-4 h-4" />}
        </button>
      </div>
      <Button type="button" onClick={onDone}>I have saved it</Button>
    </div>
  );
}

export function SdkKeysPage() {
  const { projectId = '' } = useParams();
  const keys = useSdkKeys(projectId);
  const createKey = useCreateSdkKey(projectId);
  const revokeKey = useRevokeSdkKey(projectId);
  const [name, setName] = useState('');
  const [keyType, setKeyType] = useState<SdkKeyType>('publishable');
  const [created, setCreated] = useState<CreatedSdkKey | null>(null);
  const [error, setError] = useState<string | null>(null);

  const generate = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      setCreated(await createKey.mutateAsync({ name: name.trim(), keyType }));
      setName('');
    } catch (err) {
      setError(errorText(err, 'Could not generate the key'));
    }
  };

  const revoke = async (id: number, label: string) => {
    if (!window.confirm(`Revoke "${label}"? Apps using it stop working immediately.`)) return;
    setError(null);
    try {
      await revokeKey.mutateAsync(id);
    } catch (err) {
      setError(errorText(err, 'Could not revoke the key'));
    }
  };

  return (
    <div className="p-6 space-y-6 max-w-4xl">
      <div>
        <h1 className="flex items-center gap-2 text-xl font-semibold text-text-primary"><KeyRound className="w-5 h-5" /> API Keys</h1>
        <p className="text-sm text-text-secondary mt-1">
          The SDK exchanges a key for a session. A publishable key belongs in your app's front end; a secret key acts as
          the service role and stays on your server.
        </p>
      </div>

      {error && <div className="px-4 py-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">{error}</div>}
      {created && <NewKeyNotice created={created} onDone={() => setCreated(null)} />}

      <form onSubmit={generate} className="flex flex-wrap items-end gap-3">
        <div>
          <label htmlFor="sdk-key-name" className="block text-sm text-text-secondary mb-1">Key name</label>
          <input id="sdk-key-name" value={name} maxLength={100} onChange={(e) => setName(e.target.value)}
            className="px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary" placeholder="web app" />
        </div>
        <div>
          <label htmlFor="sdk-key-type" className="block text-sm text-text-secondary mb-1">Key type</label>
          <select id="sdk-key-type" value={keyType} onChange={(e) => setKeyType(e.target.value as SdkKeyType)}
            className="px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary">
            <option value="publishable">Publishable (front end)</option>
            <option value="secret">Secret (server only)</option>
          </select>
        </div>
        <Button type="submit" disabled={createKey.isPending} className="flex items-center gap-2">
          {createKey.isPending && <Loader2 className="w-4 h-4 animate-spin" />}
          Generate key
        </Button>
      </form>

      {keys.isLoading && <Loader2 className="w-5 h-5 animate-spin text-text-secondary" />}
      {keys.isError && <p className="text-sm text-red-400">{errorText(keys.error, 'Could not load keys')}</p>}
      {keys.data?.length === 0 && <p className="text-sm text-text-secondary">No keys yet.</p>}
      {!!keys.data?.length && (
        <table className="w-full text-sm">
          <thead className="text-left text-text-secondary">
            <tr><th className="py-2">Name</th><th>Type</th><th>Key</th><th>Created</th><th /></tr>
          </thead>
          <tbody>
            {keys.data.map((key) => (
              <tr key={key.id} data-testid={`sdk-key-${key.id}`} className="border-t border-border-primary text-text-primary">
                <td className="py-2">{key.name || '—'}</td>
                <td>{key.keyType}</td>
                <td><code className="text-xs">{displayPrefix(key)}…</code></td>
                <td>{new Date(key.createdAt).toLocaleString()}</td>
                <td className="text-right">
                  <button type="button" onClick={() => revoke(key.id, key.name || displayPrefix(key))}
                    className="text-red-400 hover:text-red-300">Revoke</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <section className="space-y-3 pt-2 border-t border-border-primary" data-testid="sdk-keys-usage">
        <h2 className="text-sm font-medium text-text-primary pt-4">Use a key from code</h2>
        <ConnectSnippets projectId={projectId} />
      </section>
    </div>
  );
}
