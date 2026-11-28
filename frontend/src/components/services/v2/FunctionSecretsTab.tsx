import { useRef, useState } from 'react';
import { Trash2 } from 'lucide-react';
import { useDeleteEdgeSecret, useEdgeSecrets, useSetEdgeSecret } from '../../../hooks/useEdgeFunctions';
import { parseEnv } from '../../../utils/edgeFunctions';
import { serverErrorMessage } from '../../../utils/serverError';
import { FunctionEgressCard } from '../../FunctionEgressCard';
import { primaryButton } from '../../containers/ContainerBits';
import { ConfirmModal } from '../../ui/ConfirmModal';
import { Section } from './sections';

const input = 'w-full rounded-lg border border-border-primary bg-bg-primary px-3 py-2 text-sm text-text-primary focus:outline-none focus:ring-2 focus:ring-purple-500';

function AddSecret({ projectId }: { readonly projectId: string }) {
  const save = useSetEdgeSecret(projectId);
  const [key, setKey] = useState('');
  const [value, setValue] = useState('');
  const [paste, setPaste] = useState('');
  const [status, setStatus] = useState<string | null>(null);
  const [importing, setImporting] = useState(false);
  const [warning, setWarning] = useState<string | null>(null);
  // A fast double click lands twice before isPending re-renders the button.
  const inFlight = useRef(false);

  const submit = () => {
    if (inFlight.current || !key.trim() || !value.trim()) return;
    inFlight.current = true;
    setWarning(null);
    save.mutate({ key, value }, {
      onSuccess: (data) => {
        setKey('');
        setValue('');
        setWarning(data?.warning ?? null);
      },
      onSettled: () => { inFlight.current = false; },
    });
  };

  // One at a time, in paste order: each save rewrites the function secrets.
  const importEnv = async () => {
    if (inFlight.current) return;
    const { entries, errors } = parseEnv(paste);
    if (entries.length === 0) {
      setStatus(errors.length > 0 ? errors.join('; ') : 'nothing to import');
      return;
    }
    inFlight.current = true;
    setImporting(true);
    setStatus(null);
    const failed = await entries.reduce<Promise<string[]>>(
      (prior, entry) => prior.then((soFar) =>
        save.mutateAsync(entry).then(() => soFar, (err: unknown) => [...soFar, `${entry.key}: ${serverErrorMessage(err, 'not saved')}`])),
      Promise.resolve([]),
    );
    inFlight.current = false;
    setImporting(false);
    const parts = [`saved ${entries.length - failed.length}/${entries.length}`];
    if (errors.length > 0) parts.push(`${errors.length} skipped`);
    if (failed.length > 0) parts.push(`${failed.length} failed (${failed.join('; ')})`);
    setStatus(parts.join(', '));
    if (failed.length === 0 && errors.length === 0) setPaste('');
  };

  return (
    <div className="space-y-3">
      <p className="text-xs text-text-tertiary">
        Secrets are injected as environment variables: read one with <code>Deno.env.get(&apos;KEY&apos;)</code>. Saving redeploys the functions.
      </p>
      <div>
        <label htmlFor="fn-secret-key" className="mb-1 block text-xs text-text-tertiary">Key</label>
        <input id="fn-secret-key" value={key} onChange={(e) => setKey(e.target.value.toUpperCase())} placeholder="STRIPE_KEY" className={`${input} font-mono`} />
        <p className="mt-1 text-xs text-text-tertiary">Up to 64 letters, digits and underscores, not starting with a digit.</p>
      </div>
      <div>
        <label htmlFor="fn-secret-value" className="mb-1 block text-xs text-text-tertiary">Value</label>
        <input id="fn-secret-value" type="password" value={value} onChange={(e) => setValue(e.target.value)} className={input} />
      </div>
      <button type="button" onClick={submit} disabled={save.isPending || !key.trim() || !value.trim()} className={primaryButton}>
        {save.isPending ? 'Saving…' : 'Save secret'}
      </button>
      {warning && <p role="status" className="text-xs text-yellow-400">{warning}</p>}
      {save.error != null && <p role="alert" className="text-xs text-red-400">{serverErrorMessage(save.error, 'The secret was not saved')}</p>}
      <div className="space-y-2 border-t border-border-primary pt-3">
        <label htmlFor="fn-secret-paste" className="block text-xs text-text-tertiary">Paste .env (bulk import)</label>
        <textarea id="fn-secret-paste" rows={5} value={paste} onChange={(e) => setPaste(e.target.value)} className={`${input} font-mono text-xs`} />
        <button type="button" onClick={importEnv} disabled={importing || !paste.trim()} className={primaryButton}>
          {importing ? 'Importing…' : 'Import from .env'}
        </button>
        {status && <p className="font-mono text-xs text-text-tertiary">{status}</p>}
      </div>
    </div>
  );
}

function SecretKeys({ projectId }: { readonly projectId: string }) {
  const { data: secrets = [], error: readError } = useEdgeSecrets(projectId);
  const remove = useDeleteEdgeSecret(projectId);
  const [target, setTarget] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [warning, setWarning] = useState<string | null>(null);
  const confirm = () => {
    if (!target) return;
    setError(null);
    setWarning(null);
    remove.mutate(target, {
      onSuccess: (data) => {
        setTarget(null);
        setWarning(data?.warning ?? null);
      },
      onError: (err) => {
        setError(serverErrorMessage(err, `${target} was not deleted`));
        setTarget(null);
      },
    });
  };
  return (
    <div className="space-y-2">
      <p className="text-xs text-text-tertiary">Existing keys (values are never shown)</p>
      {readError != null && <p role="alert" className="text-xs text-red-400">{serverErrorMessage(readError, 'The secrets could not be read')}</p>}
      {error && <p role="alert" className="text-xs text-red-400">{error}</p>}
      {warning && <p role="status" className="text-xs text-yellow-400">{warning}</p>}
      {readError == null && secrets.length === 0 && <p className="text-xs text-text-tertiary">No secrets set</p>}
      <ul className="divide-y divide-border-primary">
        {secrets.map((secret) => (
          <li key={secret.key} className="flex items-center justify-between py-2">
            <code className="text-xs text-text-primary">{secret.key}</code>
            <button type="button" aria-label={`Delete ${secret.key}`} onClick={() => setTarget(secret.key)} className="text-red-400 hover:text-red-300">
              <Trash2 className="h-4 w-4" aria-hidden="true" />
            </button>
          </li>
        ))}
      </ul>
      <ConfirmModal
        open={target !== null}
        onClose={() => setTarget(null)}
        onConfirm={confirm}
        title="Delete secret"
        message={`Functions lose ${target} and are redeployed.`}
        confirmLabel="Delete"
        destructive
        loading={remove.isPending}
      />
    </div>
  );
}

// Only offered to developers: the server refuses a viewer every call here.
export function FunctionSecretsTab({ projectId }: { readonly projectId: string }) {
  return (
    <div className="space-y-6" data-testid="function-secrets">
      <Section id="fn-secrets-add" title="Add a secret">
        <AddSecret projectId={projectId} />
      </Section>
      <Section id="fn-secrets-keys" title="Secrets">
        <SecretKeys projectId={projectId} />
      </Section>
      <FunctionEgressCard projectId={projectId} />
    </div>
  );
}
