import { useState } from 'react';
import { useGuestCaptcha, useSaveAuthSettings } from '../../api/authSettings';
import type { AuthSettings } from '../../api/authSettings';
import { serverErrorMessage } from '../../utils/serverError';

// GuestSignInCard switches guest accounts and holds their optional Turnstile
// secret (EXC-662). The PUT leaves the other sign-in fields to the server.
export function GuestSignInCard({ projectId, settings }: { readonly projectId: string; readonly settings: AuthSettings }) {
  const save = useSaveAuthSettings(projectId);
  const captcha = useGuestCaptcha(projectId);
  const [secret, setSecret] = useState('');
  const enabled = settings.guestSignInEnabled ?? false;
  const captchaSet = settings.guestCaptchaSecretSet ?? false;
  const failure = save.error ?? captcha.save.error ?? captcha.remove.error;

  const toggle = (next: boolean) => save.mutate({
    siteUrl: settings.siteUrl,
    requireEmailVerification: settings.requireEmailVerification,
    guestSignInEnabled: next,
  });

  const saveSecret = (event: React.FormEvent) => {
    event.preventDefault();
    if (secret.trim() === '') return;
    captcha.save.mutate(secret.trim(), { onSuccess: () => setSecret('') });
  };

  return (
    <section className="rounded-lg border border-border-primary bg-surface-card p-4 space-y-3" data-testid="guest-sign-in-card">
      <h4 className="text-base font-semibold text-text-primary">Guest sign-in</h4>
      <label htmlFor="guest-sign-in-enabled" className="flex items-start gap-2 text-sm text-text-primary">
        <input id="guest-sign-in-enabled" type="checkbox" className="mt-1" checked={enabled} disabled={save.isPending}
          onChange={(e) => toggle(e.target.checked)} />
        <span>
          <span className="block">Guest accounts</span>
          <span className="block text-xs text-text-secondary">
            Your app can start a session with no address or password. The account runs as the role guest and reaches
            nothing until you give that role permissions. It keeps its id when the user later adds an address or a
            provider. Guests unused for 30 days are deleted.
          </span>
        </span>
      </label>

      <form onSubmit={saveSecret} className="space-y-2">
        <label htmlFor="guest-captcha-secret" className="block text-sm text-text-primary">Turnstile secret key</label>
        <p data-testid="guest-captcha-state" className="text-xs text-text-secondary">
          {captchaSet
            ? 'Set. Turnstile is required: your app sends a Turnstile token with each guest sign-in.'
            : 'No Turnstile secret. Guest sign-in is limited only by its rate limits.'}
        </p>
        <div className="flex gap-2">
          <input id="guest-captcha-secret" type="password" autoComplete="off" spellCheck={false} value={secret}
            onChange={(e) => setSecret(e.target.value)} placeholder={captchaSet ? 'Enter a new key to replace it' : '0x4AAAA…'}
            className="flex-1 px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm" />
          <button type="submit" disabled={captcha.save.isPending || secret.trim() === ''}
            className="px-3 py-2 text-sm rounded-lg bg-purple-500 hover:bg-purple-600 text-white disabled:opacity-50">
            Save secret
          </button>
          {captchaSet && (
            <button type="button" disabled={captcha.remove.isPending} onClick={() => captcha.remove.mutate()}
              className="px-3 py-2 text-sm rounded-lg text-red-400 hover:bg-red-500/10 disabled:opacity-50">
              Remove secret
            </button>
          )}
        </div>
      </form>
      {failure && <p className="text-sm text-red-400" role="alert">{serverErrorMessage(failure, 'The setting was not saved')}</p>}
    </section>
  );
}
