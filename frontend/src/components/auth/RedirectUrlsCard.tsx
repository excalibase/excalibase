import { useState } from 'react';
import { redirectUrlProblem, useSaveAuthSettings } from '../../api/authSettings';
import type { AuthSettings } from '../../api/authSettings';
import { AllowList } from '../AllowList';

// RedirectUrlsCard edits where auth may send a user back after a provider
// sign-in or an emailed link, and whether sign-in may create new users (EXC-662). Each change
// saves the whole settings body; the server validates and canonicalises.
export function RedirectUrlsCard({ projectId, settings }: { readonly projectId: string; readonly settings: AuthSettings }) {
  const save = useSaveAuthSettings(projectId);
  const [problem, setProblem] = useState<Error | null>(null);
  const redirectUrls = settings.redirectUrls ?? [];
  const allowNewUsers = settings.allowNewUsers ?? true;

  const saveWith = (change: Pick<AuthSettings, 'redirectUrls' | 'allowNewUsers'>, done?: () => void) => {
    setProblem(null);
    save.mutate(
      {
        siteUrl: settings.siteUrl,
        requireEmailVerification: settings.requireEmailVerification,
        redirectUrls,
        allowNewUsers,
        ...change,
      },
      { onSuccess: () => done?.() },
    );
  };

  const add = (entry: string, done: () => void) => {
    const reason = redirectUrlProblem(entry);
    if (reason) {
      save.reset();
      setProblem(new Error(reason));
      return;
    }
    saveWith({ redirectUrls: [...redirectUrls, entry.trim()] }, done);
  };

  return (
    <section className="rounded-lg border border-border-primary bg-surface-card p-4 space-y-3" data-testid="redirect-urls-card">
      <h4 className="text-base font-semibold text-text-primary">Redirect URLs</h4>
      <p className="text-xs text-text-secondary">
        After a provider sign-in, and in an emailed sign-in link, auth sends the user only to the site URL or to an
        address listed here, matched exactly (scheme, host, port and path). Wildcards are not supported.
      </p>
      <AllowList
        name="redirect-urls"
        entries={redirectUrls}
        emptyText="No redirect URL yet: sign-in returns only to the site URL."
        inputLabel="Redirect URL"
        placeholder="https://app.example.com/auth/callback"
        addLabel="Add URL"
        busy={save.isPending}
        error={problem ?? save.error}
        onAdd={add}
        onRemove={(entry) => saveWith({ redirectUrls: redirectUrls.filter((item) => item !== entry) })}
      />
      <label htmlFor="allow-new-users" className="flex items-start gap-2 text-sm text-text-primary">
        <input id="allow-new-users" type="checkbox" className="mt-1" checked={allowNewUsers} disabled={save.isPending}
          onChange={(e) => saveWith({ allowNewUsers: e.target.checked })} />
        <span>
          <span className="block">Allow new users</span>
          <span className="block text-xs text-text-secondary">
            Off: only people who already have an account can sign in; a first sign-in with a provider or an emailed link is refused.
          </span>
        </span>
      </label>
    </section>
  );
}
