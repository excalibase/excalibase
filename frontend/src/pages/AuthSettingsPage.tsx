import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { AlertTriangle, Loader2 } from 'lucide-react';
import { SITE_URL_RULE, siteUrlProblem, suggestSiteUrl, useAuthSettings, useSaveAuthSettings } from '../api/authSettings';
import type { AuthSettings } from '../api/authSettings';
import { useApps } from '../api/apps';
import { Button } from '../components/Button';
import { serverErrorMessage } from '../utils/serverError';

const inputClass = 'w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm';

function SiteUrlForm({ projectId, initial }: { readonly projectId: string; readonly initial: AuthSettings }) {
  const save = useSaveAuthSettings(projectId);
  const { data: apps } = useApps(projectId);
  const [siteUrl, setSiteUrl] = useState(initial.siteUrl);
  const [requireVerification, setRequireVerification] = useState(initial.requireEmailVerification);
  const [saved, setSaved] = useState(false);

  const problem = siteUrlProblem(siteUrl);
  const missing = siteUrl.trim() === '';
  const blocked = requireVerification && missing;
  const suggestion = suggestSiteUrl(apps);

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (problem || blocked) return;
    setSaved(false);
    save.mutate(
      { siteUrl: siteUrl.trim(), requireEmailVerification: requireVerification },
      { onSuccess: () => setSaved(true) },
    );
  };

  return (
    <form onSubmit={submit} className="space-y-5" data-testid="auth-settings-form">
      {initial.siteUrl === '' && (
        <div role="alert" data-testid="site-url-warning"
          className="flex gap-3 p-4 rounded-lg border border-amber-500/50 bg-amber-500/10 text-sm text-text-primary">
          <AlertTriangle className="w-5 h-5 text-amber-400 shrink-0" />
          <p>
            No site URL is set. Until you set one, sign-up (when email verification is required), resend verification
            and forgot password answer with an error and send no email, because their links need an address to point to.
          </p>
        </div>
      )}

      <div>
        <label htmlFor="site-url" className="block text-base font-semibold text-text-primary mb-1">Site URL</label>
        <p className="text-sm text-text-secondary mb-2">
          Where your app lives. Verification and password-reset emails link to it:
          {' '}<code className="text-text-primary">{'<site URL>/verify?token=…'}</code> and
          {' '}<code className="text-text-primary">{'<site URL>/reset-password?token=…'}</code>.
        </p>
        <input id="site-url" value={siteUrl} onChange={(e) => { setSiteUrl(e.target.value); setSaved(false); }}
          className={inputClass} placeholder="https://app.example.com" autoComplete="off" spellCheck={false}
          aria-invalid={problem !== null} aria-describedby="site-url-help" />
        <p id="site-url-help" className={`text-xs mt-1 ${problem ? 'text-red-400' : 'text-text-secondary'}`} data-testid="site-url-help">
          {problem ?? SITE_URL_RULE}
        </p>
        {suggestion && suggestion !== siteUrl.trim() && (
          <p className="text-sm text-text-secondary mt-2">
            Your app is at <code className="text-text-primary break-all">{suggestion}</code>.{' '}
            <button type="button" className="underline text-purple-400" onClick={() => { setSiteUrl(suggestion); setSaved(false); }}>
              Use this address
            </button>
          </p>
        )}
      </div>

      <label htmlFor="require-verification" className="flex items-start gap-2 text-sm text-text-primary">
        <input id="require-verification" type="checkbox" checked={requireVerification} className="mt-1"
          onChange={(e) => { setRequireVerification(e.target.checked); setSaved(false); }} />
        <span>
          Require email verification
          <span className="block text-xs text-text-secondary">Users must confirm their address before they can sign in. Needs a site URL.</span>
        </span>
      </label>
      {blocked && <p className="text-sm text-red-400" data-testid="verification-needs-url">Set the site URL before requiring email verification.</p>}

      {save.isError && <p className="text-sm text-red-400" role="alert">{serverErrorMessage(save.error, 'The settings were not saved')}</p>}
      {saved && <p className="text-sm text-text-secondary" data-testid="auth-settings-saved">Saved. Auth uses it for the next email it sends, after it refreshes the project's settings.</p>}
      <Button type="submit" disabled={save.isPending || problem !== null || blocked}>Save</Button>
    </form>
  );
}

export function AuthSettingsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data, isLoading, isError } = useAuthSettings(projectId || '');

  if (isLoading) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }
  return (
    <div data-testid="auth-settings-page" className="max-w-2xl">
      <h3 className="text-lg font-semibold text-text-primary mb-4">Auth Settings</h3>
      {isError || !data
        ? <p className="text-sm text-red-400" role="alert">Could not load the auth settings.</p>
        : <SiteUrlForm projectId={projectId || ''} initial={data} />}
    </div>
  );
}
