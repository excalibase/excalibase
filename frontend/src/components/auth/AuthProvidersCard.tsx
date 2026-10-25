import { useState } from 'react';
import { Loader2 } from 'lucide-react';
import { providerUpdateBody, useAuthProviders, useSaveAuthProvider } from '../../api/authProviders';
import type { AuthProvider } from '../../api/authProviders';
import { Button } from '../Button';
import { StringRow } from '../ConnectionParts';
import { serverErrorMessage } from '../../utils/serverError';

interface ProviderGuide {
  readonly name: string;
  readonly consoleName: string;
  readonly consoleUrl: string;
  readonly steps: string;
  readonly idSample: string;
}

const guides: Record<string, ProviderGuide> = {
  google: {
    name: 'Google',
    consoleName: 'Google Cloud console',
    consoleUrl: 'https://console.cloud.google.com/apis/credentials',
    steps: 'Create an OAuth client ID of type "Web application" and add the callback URL below as an authorized redirect URI.',
    idSample: '1234567890-abc123.apps.googleusercontent.com',
  },
  github: {
    name: 'GitHub',
    consoleName: 'GitHub Developer settings',
    consoleUrl: 'https://github.com/settings/developers',
    steps: 'Create a new OAuth App and use the callback URL below as its authorization callback URL.',
    idSample: 'Ov23liAbCdEf12345678',
  },
};

const inputClass = 'w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm';

function guideFor(provider: string): ProviderGuide {
  return guides[provider] ?? { name: provider, consoleName: provider, consoleUrl: '', steps: '', idSample: '' };
}

function ProviderForm({ projectId, initial }: { readonly projectId: string; readonly initial: AuthProvider }) {
  const save = useSaveAuthProvider(projectId, initial.provider);
  const [enabled, setEnabled] = useState(initial.enabled);
  const [clientId, setClientId] = useState(initial.clientId);
  const [secret, setSecret] = useState('');
  const [saved, setSaved] = useState(false);
  const guide = guideFor(initial.provider);
  const prefix = `auth-provider-${initial.provider}`;

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    setSaved(false);
    save.mutate(providerUpdateBody(enabled, clientId, secret), {
      onSuccess: () => {
        setSecret('');
        setSaved(true);
      },
    });
  };

  return (
    <form data-testid={prefix} onSubmit={submit} className="space-y-3 p-4 rounded-lg border border-border-primary">
      <div className="flex items-center justify-between">
        <h4 className="font-medium text-text-primary">{guide.name}</h4>
        <label htmlFor={`${prefix}-enabled`} className="flex items-center gap-2 text-sm text-text-secondary">
          <input id={`${prefix}-enabled`} type="checkbox" checked={enabled} aria-label={`Enable ${guide.name}`}
            onChange={(e) => { setEnabled(e.target.checked); setSaved(false); }} />
          <span>Enabled</span>
        </label>
      </div>
      <p className="text-xs text-text-secondary">
        In the{' '}
        <a href={guide.consoleUrl} target="_blank" rel="noreferrer" className="underline text-purple-400">{guide.consoleName}</a>
        : {guide.steps}
      </p>
      <StringRow testId={`${prefix}-callback`} label={`Callback URL to register at ${guide.name}`}
        shown={initial.callbackUrl} copied={initial.callbackUrl} />
      <div>
        <label htmlFor={`${prefix}-client-id`} className="block text-sm text-text-secondary mb-1">Client ID</label>
        <input id={`${prefix}-client-id`} value={clientId} className={inputClass} autoComplete="off" spellCheck={false}
          placeholder={guide.idSample} onChange={(e) => { setClientId(e.target.value); setSaved(false); }} />
      </div>
      <div>
        <label htmlFor={`${prefix}-secret`} className="block text-sm text-text-secondary mb-1">Client secret</label>
        <input id={`${prefix}-secret`} type="password" value={secret} className={inputClass} autoComplete="new-password"
          placeholder={initial.clientSecretSet ? 'Leave empty to keep the current secret' : `From the OAuth app at ${guide.name}`}
          onChange={(e) => { setSecret(e.target.value); setSaved(false); }} />
        <p className="text-xs text-text-secondary mt-1" data-testid={`${prefix}-secret-state`}>
          {initial.clientSecretSet ? 'Secret set. It is never shown again.' : 'Secret not set.'}
        </p>
      </div>
      {save.isError && (
        <p className="text-sm text-red-400" role="alert">{serverErrorMessage(save.error, 'The provider was not saved')}</p>
      )}
      {saved && <p className="text-sm text-text-secondary">Saved. Auth uses it after it refreshes the project's settings.</p>}
      <Button type="submit" disabled={save.isPending} aria-label={`Save ${guide.name}`}>Save</Button>
    </form>
  );
}

// AuthProvidersCard lets a project Admin switch on Google and GitHub sign-in
// for the app's own users with the project's own OAuth apps.
export function AuthProvidersCard({ projectId }: { readonly projectId: string }) {
  const { data, isLoading, isError, error } = useAuthProviders(projectId, true);
  return (
    <section className="space-y-3" data-testid="auth-providers">
      <h4 className="text-base font-semibold text-text-primary">Sign-in providers</h4>
      <p className="text-sm text-text-secondary">
        Let your app's users sign in with Google or GitHub. Create an OAuth app at the provider with the callback
        URL shown, then paste its client ID and secret here. Only project Admins can save a provider.
      </p>
      {isLoading && <Loader2 className="w-4 h-4 animate-spin" />}
      {isError && (
        <p className="text-sm text-red-400" role="alert">{serverErrorMessage(error, 'The sign-in providers could not be read')}</p>
      )}
      <div className="grid gap-4">
        {data?.map((provider) => <ProviderForm key={provider.provider} projectId={projectId} initial={provider} />)}
      </div>
    </section>
  );
}
