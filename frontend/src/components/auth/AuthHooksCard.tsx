import { useState } from 'react';
import { Loader2 } from 'lucide-react';
import { useAuthHooks, useRemoveAuthHookSecret, useRotateAuthHookSecret, useSaveAuthHook } from '../../api/authHooks';
import type { AuthHook, AuthHookName } from '../../api/authHooks';
import { Button } from '../Button';
import { StringRow } from '../ConnectionParts';
import { serverErrorMessage } from '../../utils/serverError';

const hookGuides: Record<AuthHookName, { readonly name: string; readonly does: string }> = {
  custom_access_token: {
    name: 'Custom access token',
    does: "Adds claims to every access token, and may pick a default role among the user's allowed roles.",
  },
  before_user_created: {
    name: 'Before user created',
    does: 'Allows or denies each new account before it is created, for example to keep sign-up to your own domain.',
  },
  send_email: {
    name: 'Send email',
    does: "Delivers the project's auth mail through your own mailer instead of Excalibase's.",
  },
};

const inputClass = 'w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm';

function HookForm({ projectId, hook, secretSet }: { readonly projectId: string; readonly hook: AuthHook; readonly secretSet: boolean }) {
  const save = useSaveAuthHook(projectId, hook.hook);
  const [enabled, setEnabled] = useState(hook.enabled);
  const [url, setUrl] = useState(hook.url);
  const [saved, setSaved] = useState(false);
  const guide = hookGuides[hook.hook];
  const prefix = `auth-hook-${hook.hook}`;
  const blocked = !secretSet && !enabled;

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    setSaved(false);
    save.mutate({ enabled, url: url.trim() }, { onSuccess: () => setSaved(true) });
  };

  return (
    <form data-testid={prefix} onSubmit={submit} className="space-y-2 p-4 rounded-lg border border-border-primary">
      <div className="flex items-center justify-between">
        <h5 className="font-medium text-text-primary">{guide.name}</h5>
        <label htmlFor={`${prefix}-enabled`} className="flex items-center gap-2 text-sm text-text-secondary">
          <input id={`${prefix}-enabled`} type="checkbox" checked={enabled} disabled={blocked} aria-label={`Enable ${guide.name}`}
            onChange={(e) => { setEnabled(e.target.checked); setSaved(false); }} />
          <span>Enabled</span>
        </label>
      </div>
      <p className="text-xs text-text-secondary">{guide.does}</p>
      {blocked && <p className="text-xs text-amber-400">Generate a signing secret first to switch hooks on.</p>}
      <label htmlFor={`${prefix}-url`} className="block text-sm text-text-secondary">{guide.name} URL</label>
      <input id={`${prefix}-url`} value={url} className={inputClass} autoComplete="off" spellCheck={false}
        placeholder="https://hooks.example.com/auth" onChange={(e) => { setUrl(e.target.value); setSaved(false); }} />
      <p className="text-xs text-text-secondary">An https:// URL on a public host, port 443 only.</p>
      {save.isError && <p className="text-sm text-red-400" role="alert">{serverErrorMessage(save.error, 'The hook was not saved')}</p>}
      {saved && <p className="text-sm text-text-secondary">Saved. Auth uses it after it refreshes the project's settings.</p>}
      <Button type="submit" size="sm" disabled={save.isPending} aria-label={`Save ${guide.name}`}>Save</Button>
    </form>
  );
}

function SigningSecret({ projectId, secretSet }: { readonly projectId: string; readonly secretSet: boolean }) {
  const rotate = useRotateAuthHookSecret(projectId);
  const remove = useRemoveAuthHookSecret(projectId);
  const [issued, setIssued] = useState<{ secret: string; rotated: boolean } | null>(null);

  const generate = () => {
    if (secretSet && !window.confirm('Rotate the signing secret? The old one stops working at once: enabled hooks, and with them sign-in, fail until your endpoint uses the new secret.')) return;
    rotate.mutate(undefined, { onSuccess: (secret) => setIssued({ secret, rotated: secretSet }) });
  };

  const removeSecret = () => {
    if (!window.confirm('Remove the signing secret? Every hook is switched off first.')) return;
    remove.mutate(undefined, { onSuccess: () => setIssued(null) });
  };

  const failure = rotate.error ?? remove.error;
  return (
    <div className="space-y-2 p-4 rounded-lg border border-border-primary">
      <div className="flex items-center justify-between">
        <h5 className="font-medium text-text-primary">Signing secret</h5>
        <span data-testid="auth-hooks-secret-state" className="text-xs text-text-secondary">{secretSet ? 'Set' : 'Not set'}</span>
      </div>
      {issued && (
        <div data-testid="auth-hooks-new-secret" className="space-y-1">
          <StringRow testId="auth-hooks-secret" label="Hook signing secret" shown={issued.secret} copied={issued.secret} />
          <p className="text-xs text-amber-400">
            Copy it now: it is not shown again.
            {issued.rotated && ' Rotating breaks every enabled hook until your endpoint verifies with this new secret.'}
          </p>
        </div>
      )}
      {failure && <p className="text-sm text-red-400" role="alert">{serverErrorMessage(failure, 'The secret was not changed')}</p>}
      <div className="flex gap-2">
        <Button type="button" size="sm" disabled={rotate.isPending} onClick={generate}>{secretSet ? 'Rotate secret' : 'Generate secret'}</Button>
        {secretSet && (
          <Button type="button" size="sm" variant="danger" disabled={remove.isPending} onClick={removeSecret}>Remove secret</Button>
        )}
      </div>
    </div>
  );
}

// AuthHooksCard lets a project Admin point the auth service at the project's
// own endpoints and hold the secret those calls are signed with (EXC-662).
export function AuthHooksCard({ projectId }: { readonly projectId: string }) {
  const { data, isLoading, isError, error } = useAuthHooks(projectId);
  return (
    <section className="space-y-3" data-testid="auth-hooks-card">
      <h4 className="text-base font-semibold text-text-primary">Auth hooks</h4>
      <p className="text-sm text-text-secondary">
        Auth calls your endpoint with an HTTPS POST signed in the Standard Webhooks format: the headers webhook-id,
        webhook-timestamp and webhook-signature. Verify the signature with the signing secret, and refuse a timestamp
        more than 5 minutes off or an id you have seen. A hook that fails or takes over 2 s makes the sign-in fail.
        Only project Admins can change hooks.
      </p>
      {isLoading && <Loader2 className="w-4 h-4 animate-spin" />}
      {isError && <p className="text-sm text-red-400" role="alert">{serverErrorMessage(error, 'The auth hooks could not be read')}</p>}
      {data && (
        <div className="grid gap-4">
          <SigningSecret projectId={projectId} secretSet={data.secretSet} />
          {/* Keyed on the secret too: removing it switches every hook off on the server. */}
          {data.hooks.map((hook) => (
            <HookForm key={`${hook.hook}-${data.secretSet}`} projectId={projectId} hook={hook} secretSet={data.secretSet} />
          ))}
        </div>
      )}
    </section>
  );
}
