import { useSaveAuthSettings } from '../../api/authSettings';
import type { AuthSettings } from '../../api/authSettings';
import { serverErrorMessage } from '../../utils/serverError';

// EmailSignInCard switches sign-in by emailed link or eight-digit code (EXC-662).
// The PUT leaves out the redirect list and the new-user switch, so the server
// keeps them as stored.
export function EmailSignInCard({ projectId, settings }: { readonly projectId: string; readonly settings: AuthSettings }) {
  const save = useSaveAuthSettings(projectId);
  const enabled = settings.emailOtpEnabled ?? false;

  const toggle = (next: boolean) => save.mutate({
    siteUrl: settings.siteUrl,
    requireEmailVerification: settings.requireEmailVerification,
    emailOtpEnabled: next,
  });

  return (
    <section className="rounded-lg border border-border-primary bg-surface-card p-4 space-y-3" data-testid="email-sign-in-card">
      <h4 className="text-base font-semibold text-text-primary">Email sign-in</h4>
      <label htmlFor="email-otp-enabled" className="flex items-start gap-2 text-sm text-text-primary">
        <input id="email-otp-enabled" type="checkbox" className="mt-1" checked={enabled} disabled={save.isPending}
          onChange={(e) => toggle(e.target.checked)} />
        <span>
          <span className="block">Email links and codes</span>
          <span className="block text-xs text-text-secondary">
            Users sign in with a link or a eight-digit code sent to their address, without a password. Each works once,
            for 10 minutes, in the browser that asked for it. A link goes to the site URL or a redirect URL above.
          </span>
        </span>
      </label>
      {save.isError && <p className="text-sm text-red-400" role="alert">{serverErrorMessage(save.error, 'The setting was not saved')}</p>}
    </section>
  );
}
