import { useState } from 'react';
import { Check, Copy, KeyRound, Loader2 } from 'lucide-react';
import { Button } from '../Button';
import {
  permissionsText,
  useCreateS3Key,
  useRevokeS3Key,
  useS3Keys,
  type CreatedS3Key,
  type S3Permissions,
} from '../../api/s3Keys';

function errorText(err: unknown, fallback: string): string {
  const axiosErr = err as { response?: { data?: { error?: string } } };
  return axiosErr?.response?.data?.error || fallback;
}

function clientSettings(created: CreatedS3Key): string {
  return [
    '# aws cli',
    `aws configure set aws_access_key_id ${created.accessKeyId}`,
    `aws configure set aws_secret_access_key ${created.secretAccessKey}`,
    `aws configure set region ${created.region}`,
    'aws configure set default.s3.addressing_style path',
    `aws s3 ls --endpoint-url ${created.endpoint}`,
    '',
    '# rclone (~/.config/rclone/rclone.conf)',
    '[excalibase]',
    'type = s3',
    'provider = Other',
    `access_key_id = ${created.accessKeyId}`,
    `secret_access_key = ${created.secretAccessKey}`,
    `endpoint = ${created.endpoint}`,
    `region = ${created.region}`,
    'force_path_style = true',
  ].join('\n');
}

function NewKeyNotice({ created, onDone }: { readonly created: CreatedS3Key; readonly onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  const settings = clientSettings(created);
  const copy = async () => {
    await navigator.clipboard?.writeText(settings);
    setCopied(true);
  };
  return (
    <div data-testid="new-s3-key" className="space-y-3 p-4 rounded-lg border border-purple-500/30 bg-purple-500/10">
      <p className="text-sm text-text-primary">Copy the secret now. It will not be shown again.</p>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-text-secondary">Access key id</dt><dd><code>{created.accessKeyId}</code></dd>
        <dt className="text-text-secondary">Secret access key</dt><dd><code className="break-all">{created.secretAccessKey}</code></dd>
        <dt className="text-text-secondary">Endpoint</dt><dd><code>{created.endpoint}</code></dd>
        <dt className="text-text-secondary">Region</dt><dd><code>{created.region}</code></dd>
      </dl>
      <div className="relative">
        <pre className="px-3 py-2 rounded bg-bg-secondary text-text-primary text-xs overflow-x-auto">{settings}</pre>
        <button type="button" onClick={copy} aria-label="Copy settings" className="absolute top-2 right-2 p-1 text-text-secondary hover:text-text-primary">
          {copied ? <Check className="w-4 h-4" /> : <Copy className="w-4 h-4" />}
        </button>
      </div>
      <Button type="button" onClick={onDone}>I have saved it</Button>
    </div>
  );
}

interface CreateFormProps {
  readonly projectId: string;
  readonly buckets: ReadonlyArray<{ name: string }>;
  readonly onCreated: (created: CreatedS3Key) => void;
  readonly onError: (message: string | null) => void;
}

function CreateKeyForm({ projectId, buckets, onCreated, onError }: CreateFormProps) {
  const createKey = useCreateS3Key(projectId);
  const [name, setName] = useState('');
  const [scoped, setScoped] = useState(false);
  const [chosen, setChosen] = useState<string[]>([]);
  const [permissions, setPermissions] = useState<S3Permissions>({ read: true, write: false, delete: false });
  const [days, setDays] = useState('');

  const toggleBucket = (bucket: string) =>
    setChosen((current) => (current.includes(bucket) ? current.filter((b) => b !== bucket) : [...current, bucket]));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    onError(null);
    if (!permissions.read && !permissions.write && !permissions.delete) {
      onError('A key needs at least one of read, write and delete.');
      return;
    }
    try {
      const body = {
        name: name.trim(),
        buckets: scoped ? chosen : [],
        permissions,
        ...(days ? { expiresInDays: Number(days) } : {}),
      };
      onCreated(await createKey.mutateAsync(body));
      setName('');
      setDays('');
    } catch (err) {
      onError(errorText(err, 'Could not create the key'));
    }
  };

  return (
    <form onSubmit={submit} className="space-y-3">
      <div className="flex flex-wrap items-end gap-3">
        <div>
          <label htmlFor="s3-key-name" className="block text-sm text-text-secondary mb-1">Key name</label>
          <input id="s3-key-name" value={name} maxLength={64} required onChange={(e) => setName(e.target.value)}
            className="px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary" placeholder="rclone on the NAS" />
        </div>
        <div>
          <label htmlFor="s3-key-days" className="block text-sm text-text-secondary mb-1">Expires after (days)</label>
          <input id="s3-key-days" type="number" min={1} max={365} value={days} onChange={(e) => setDays(e.target.value)}
            className="w-32 px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary" placeholder="never" />
        </div>
      </div>
      <fieldset className="flex gap-4 text-sm text-text-primary">
        <legend className="text-text-secondary mb-1">Permissions</legend>
        {(['read', 'write', 'delete'] as const).map((p) => (
          <label key={p} className="flex items-center gap-1">
            <input type="checkbox" checked={permissions[p]} onChange={() => setPermissions((cur) => ({ ...cur, [p]: !cur[p] }))} />
            {p.charAt(0).toUpperCase() + p.slice(1)}
          </label>
        ))}
      </fieldset>
      {permissions.write && (
        <p className="text-xs text-amber-300">Write also overwrites existing objects: buckets keep no earlier versions.</p>
      )}
      <fieldset className="text-sm text-text-primary space-y-1">
        <legend className="text-text-secondary mb-1">Buckets</legend>
        <label className="flex items-center gap-1">
          <input type="radio" name="s3-scope" checked={!scoped} onChange={() => setScoped(false)} /> All buckets
        </label>
        <label className="flex items-center gap-1">
          <input type="radio" name="s3-scope" checked={scoped} onChange={() => setScoped(true)} /> Only these buckets
        </label>
        {scoped && (
          <div className="flex flex-wrap gap-3 pl-5">
            {buckets.map((b) => (
              <label key={b.name} className="flex items-center gap-1">
                <input type="checkbox" checked={chosen.includes(b.name)} onChange={() => toggleBucket(b.name)} />
                {b.name}
              </label>
            ))}
          </div>
        )}
      </fieldset>
      <Button type="submit" disabled={createKey.isPending} className="flex items-center gap-2">
        {createKey.isPending && <Loader2 className="w-4 h-4 animate-spin" />}
        Create key
      </Button>
    </form>
  );
}

interface S3AccessPanelProps {
  readonly projectId: string;
  readonly buckets: ReadonlyArray<{ name: string }>;
}

// S3AccessPanel issues and revokes the keys S3 clients use on the project's buckets.
export function S3AccessPanel({ projectId, buckets }: S3AccessPanelProps) {
  const keys = useS3Keys(projectId);
  const revokeKey = useRevokeS3Key(projectId);
  const [created, setCreated] = useState<CreatedS3Key | null>(null);
  const [error, setError] = useState<string | null>(null);
  const listed = keys.data?.keys ?? [];

  const revoke = async (id: string, label: string) => {
    if (!window.confirm(`Revoke "${label}"? Every S3 client using it is refused from its next request.`)) return;
    setError(null);
    try {
      await revokeKey.mutateAsync(id);
    } catch (err) {
      setError(errorText(err, 'Could not revoke the key'));
    }
  };

  return (
    <div data-testid="s3-access-panel" className="p-6 space-y-6 overflow-y-auto">
      <div>
        <h2 className="flex items-center gap-2 text-lg font-semibold text-text-primary"><KeyRound className="w-5 h-5" /> S3 access</h2>
        <p className="text-sm text-text-secondary mt-1">
          Any S3 client (aws cli, rclone, the AWS SDKs) can reach these buckets with a key, path-style, at{' '}
          <code data-testid="s3-endpoint">{keys.data?.endpoint || 'the S3 endpoint'}</code>. Bucket size limits, file types and the
          project's storage quota still apply; the app-user access rules do not.
        </p>
      </div>
      {error && <div role="alert" className="px-4 py-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">{error}</div>}
      {created && <NewKeyNotice created={created} onDone={() => setCreated(null)} />}
      <CreateKeyForm projectId={projectId} buckets={buckets} onCreated={setCreated} onError={setError} />
      {keys.isLoading && <Loader2 className="w-5 h-5 animate-spin text-text-secondary" />}
      {keys.isError && <p className="text-sm text-red-400">{errorText(keys.error, 'Could not load keys')}</p>}
      {keys.isSuccess && listed.length === 0 && <p className="text-sm text-text-secondary">No keys yet.</p>}
      {listed.length > 0 && (
        <table className="w-full text-sm">
          <thead className="text-left text-text-secondary">
            <tr><th className="py-2">Name</th><th>Access key id</th><th>Buckets</th><th>Permissions</th><th>Last used</th><th /></tr>
          </thead>
          <tbody>
            {listed.map((key) => (
              <tr key={key.id} data-testid={`s3-key-${key.id}`} className="border-t border-border-primary text-text-primary">
                <td className="py-2">{key.name}</td>
                <td><code className="text-xs">{key.accessKeyId}</code></td>
                <td>{key.buckets.length === 0 ? 'All buckets' : key.buckets.join(', ')}</td>
                <td>{permissionsText(key)}</td>
                <td>{key.lastUsedAt ? new Date(key.lastUsedAt).toLocaleString() : 'never'}</td>
                <td className="text-right">
                  <button type="button" onClick={() => revoke(key.id, key.name)} className="text-red-400 hover:text-red-300">Revoke</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
