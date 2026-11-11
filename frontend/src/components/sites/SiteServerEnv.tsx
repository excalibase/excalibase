import { useState } from 'react';
import { apiErrorMessage } from '../../api/apps';
import {
  useServerSecrets,
  useSetServerSecrets,
  useSetSiteEnv,
  useSiteEnv,
} from '../../api/siteServer';
import { SecretChecklist } from './SecretChecklist';
import { primaryButton, secondaryButton, Spinner } from '../containers/ContainerBits';

function ServerSecrets({ projectId }: { readonly projectId: string }) {
  const { data: secrets } = useServerSecrets(projectId);
  const update = useSetServerSecrets(projectId);
  if (!secrets || secrets.length === 0) return null;
  const visible = secrets.filter((secret) => secret.serverVisible).map((secret) => secret.name);
  const toggle = (name: string, on: boolean) =>
    update.mutate(on ? [...visible, name] : visible.filter((kept) => kept !== name));
  return (
    <div className="space-y-2" data-testid="server-secrets">
      <h5 className="text-xs font-semibold text-text-primary">Project secrets</h5>
      <p className="text-xs text-amber-400" data-testid="server-secrets-warning">
        A secret marked here is handed to this project&apos;s server sites by name, and the code you
        deploy reads it: it runs in that Worker. A rollback uses the secret&apos;s current value.
        Mark only what the code needs.
      </p>
      <SecretChecklist
        rows={secrets.map((secret) => ({ name: secret.name, checked: secret.serverVisible }))}
        pending={update.isPending}
        error={update.error}
        testIdPrefix="server-secret"
        onToggle={toggle}
      />
    </div>
  );
}

// Runtime variables of a server site: plain text, readable by its code, applied on the next deploy.
export function SiteServerEnv({
  projectId,
  siteId,
}: {
  readonly projectId: string;
  readonly siteId: string;
}) {
  const { data: vars, error } = useSiteEnv(projectId, siteId);
  const save = useSetSiteEnv(projectId, siteId);
  const [name, setName] = useState('');
  const [value, setValue] = useState('');
  if (error)
    return (
      <p className="text-xs text-red-400">
        {apiErrorMessage(error, 'The variables could not be loaded')}
      </p>
    );
  if (!vars) return <Spinner />;
  const add = () => {
    if (name.trim() === '') return;
    save.mutate(
      { ...vars, [name.trim()]: value },
      { onSuccess: () => (setName(''), setValue('')) },
    );
  };
  const remove = (key: string) => {
    const rest = Object.fromEntries(Object.entries(vars).filter(([existing]) => existing !== key));
    save.mutate(rest);
  };
  return (
    <div className="space-y-3" data-testid="site-server-env">
      <p className="text-xs text-text-tertiary" data-testid="env-platform-note">
        The platform sets EXCALIBASE_URL and EXCALIBASE_PROJECT_ID itself. Set
        EXCALIBASE_PUBLISHABLE_KEY to your project&apos;s publishable key so the code can call the
        API. Variables are plain text, visible to anyone who can edit this site; use a secret below
        for anything private.
      </p>
      <ul className="space-y-1">
        {Object.entries(vars)
          .sort(([left], [right]) => left.localeCompare(right))
          .map(([key, current]) => (
            <li key={key} className="flex items-center gap-2">
              <code className="text-sm text-text-primary">{key}</code>
              <input
                readOnly
                value={current}
                data-testid={`env-value-${key}`}
                className="flex-1 rounded border border-border bg-surface px-2 py-1 text-sm text-text-primary"
              />
              <button
                type="button"
                className={secondaryButton}
                disabled={save.isPending}
                onClick={() => remove(key)}
                data-testid={`env-remove-${key}`}
              >
                Remove
              </button>
            </li>
          ))}
      </ul>
      <div className="flex items-center gap-2">
        <input
          value={name}
          onChange={(event) => setName(event.target.value)}
          placeholder="NAME"
          aria-label="Variable name"
          data-testid="env-new-name"
          className="rounded border border-border bg-surface px-2 py-1 text-sm text-text-primary"
        />
        <input
          value={value}
          onChange={(event) => setValue(event.target.value)}
          placeholder="value"
          aria-label="Variable value"
          data-testid="env-new-value"
          className="flex-1 rounded border border-border bg-surface px-2 py-1 text-sm text-text-primary"
        />
        <button
          type="button"
          className={primaryButton}
          disabled={save.isPending}
          onClick={add}
          data-testid="env-add"
        >
          Add
        </button>
      </div>
      {save.isSuccess && (
        <p className="text-xs text-text-tertiary" data-testid="env-saved">
          Saved. The code sees the change after the next deploy.
        </p>
      )}
      {save.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(save.error, 'The change could not be saved')}
        </p>
      )}
      <ServerSecrets projectId={projectId} />
    </div>
  );
}
