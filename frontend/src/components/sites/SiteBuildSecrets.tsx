import { apiErrorMessage } from '../../api/apps';
import { useBuildSecrets, useSetBuildSecrets } from '../../api/siteBuilds';
import { Spinner } from '../containers/ContainerBits';

// Project secrets stay out of builds unless marked here; a marked one ends up in public files.
export function SiteBuildSecrets({ projectId }: { readonly projectId: string }) {
  const { data: secrets, error } = useBuildSecrets(projectId);
  const update = useSetBuildSecrets(projectId);
  if (error)
    return (
      <p className="text-xs text-red-400">
        {apiErrorMessage(error, 'The secrets could not be loaded')}
      </p>
    );
  if (!secrets) return <Spinner />;
  if (secrets.length === 0) {
    return <p className="text-sm text-text-tertiary">This project has no secrets.</p>;
  }
  const visible = secrets.filter((secret) => secret.buildVisible).map((secret) => secret.name);
  const toggle = (name: string, on: boolean) =>
    update.mutate(on ? [...visible, name] : visible.filter((kept) => kept !== name));
  return (
    <div className="space-y-2" data-testid="site-build-secrets">
      <p className="text-xs text-amber-400" data-testid="site-build-secrets-warning">
        A secret a build can see may be copied into the built files, which anyone can download. Only
        mark values meant to be public, such as a publishable key.
      </p>
      <ul className="space-y-1">
        {secrets.map((secret) => (
          <li key={secret.name}>
            <label className="flex items-center gap-2 text-sm text-text-primary">
              <input
                type="checkbox"
                checked={secret.buildVisible}
                disabled={update.isPending}
                onChange={(event) => toggle(secret.name, event.target.checked)}
                data-testid={`build-secret-${secret.name}`}
              />
              <code>{secret.name}</code>
            </label>
          </li>
        ))}
      </ul>
      {update.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(update.error, 'The change could not be saved')}
        </p>
      )}
    </div>
  );
}
