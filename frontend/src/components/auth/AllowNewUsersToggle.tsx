import { useSaveAuthSettings } from '../../api/authSettings';
import type { AuthSettings } from '../../api/authSettings';

// AllowNewUsersToggle is the registration switch (EXC-457). Auth reads it on
// every user-creating route whatever the sign-in feature flags, so Studio shows
// it whatever they are. `extra` is the part of the settings body the host card owns.
export function AllowNewUsersToggle({ projectId, settings, extra }: {
  readonly projectId: string;
  readonly settings: AuthSettings;
  readonly extra?: Partial<AuthSettings>;
}) {
  const save = useSaveAuthSettings(projectId);
  const allowNewUsers = settings.allowNewUsers ?? true;
  return (
    <label htmlFor="allow-new-users" className="flex items-start gap-2 text-sm text-text-primary">
      <input id="allow-new-users" type="checkbox" className="mt-1" checked={allowNewUsers} disabled={save.isPending}
        onChange={(e) => save.mutate({
          siteUrl: settings.siteUrl,
          requireEmailVerification: settings.requireEmailVerification,
          ...extra,
          allowNewUsers: e.target.checked,
        })} />
      <span>
        <span className="block">Allow new users</span>
        <span className="block text-xs text-text-secondary">
          Off: only people who already have an account can sign in; registration, a first sign-in with a provider or an emailed link, and guest sign-in are refused.
        </span>
        {save.isError && <span role="alert" className="block text-xs text-red-400">Could not save the setting.</span>}
      </span>
    </label>
  );
}
