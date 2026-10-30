import { useMFASwitch } from '../../api/authSettings';
import type { AuthSettings } from '../../api/authSettings';
import { serverErrorMessage } from '../../utils/serverError';

// MFACard switches end-user TOTP MFA (EXC-662, flag `authmfa`). The project's
// key is created on the server the first time it is switched on.
export function MFACard({
  projectId,
  settings,
}: {
  readonly projectId: string;
  readonly settings: AuthSettings;
}) {
  const save = useMFASwitch(projectId);
  const enabled = settings.mfaEnabled ?? false;

  return (
    <section
      className="rounded-lg border border-border-primary bg-surface-card p-4 space-y-3"
      data-testid="mfa-card"
    >
      <h4 className="text-base font-semibold text-text-primary">Multi-factor authentication</h4>
      <label htmlFor="mfa-enabled" className="flex items-start gap-2 text-sm text-text-primary">
        <input
          id="mfa-enabled"
          type="checkbox"
          className="mt-1"
          checked={enabled}
          disabled={save.isPending}
          onChange={(e) => save.mutate(e.target.checked)}
        />
        <span>
          <span className="block">Authenticator apps (TOTP)</span>
          <span className="block text-xs text-text-secondary">
            Users can add an authenticator app as a second step after they sign in, and get ten
            one-time recovery codes in case they lose it. A token then says how the user signed in:
            a permission can require <code>x-excalibase-aal</code> = <code>aal2</code> so only users
            who passed the second step reach that data. Switching this off stops asking for the
            second step; factors users added are kept.
          </span>
        </span>
      </label>
      {save.isError && (
        <p className="text-sm text-red-400" role="alert">
          {serverErrorMessage(save.error, 'The setting was not saved')}
        </p>
      )}
    </section>
  );
}
