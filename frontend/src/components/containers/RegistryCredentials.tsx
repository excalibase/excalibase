import { useState, type FormEvent } from 'react';
import { KeyRound } from 'lucide-react';
import { apiErrorMessage } from '../../api/apps';
import {
  useRegistryCredentials,
  useRemoveRegistryCredential,
  useSetRegistryCredential,
} from '../../api/registryCredentials';
import { inputClass } from './EnvVarEditor';
import { primaryButton, secondaryButton } from './ContainerBits';

function RegistryRow({
  registry,
  onRemove,
  removing,
}: {
  readonly registry: string;
  readonly onRemove: (registry: string) => void;
  readonly removing: boolean;
}) {
  const [confirming, setConfirming] = useState(false);
  return (
    <li
      className="flex items-center justify-between gap-3 px-4 py-2.5 text-sm"
      data-testid={`registry-row-${registry}`}
    >
      <span className="font-mono text-text-primary">{registry}</span>
      {confirming ? (
        <span className="flex items-center gap-2">
          <span className="text-text-secondary">Remove? Deploys from it stop working.</span>
          <button
            type="button"
            onClick={() => onRemove(registry)}
            disabled={removing}
            className={secondaryButton}
            data-testid={`registry-remove-confirm-${registry}`}
          >
            Remove
          </button>
          <button type="button" onClick={() => setConfirming(false)} className={secondaryButton}>
            Cancel
          </button>
        </span>
      ) : (
        <button
          type="button"
          onClick={() => setConfirming(true)}
          className={secondaryButton}
          data-testid={`registry-remove-${registry}`}
        >
          Remove
        </button>
      )}
    </li>
  );
}

// Credentials are write-only: the password field is cleared once sent and nothing reads one back.
export function RegistryCredentials({ projectId }: { readonly projectId: string }) {
  const { data: registries = [] } = useRegistryCredentials(projectId);
  const save = useSetRegistryCredential(projectId);
  const remove = useRemoveRegistryCredential(projectId);
  const [registry, setRegistry] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');

  const submit = (event: FormEvent) => {
    event.preventDefault();
    save.mutate(
      { registry: registry.trim(), username, password },
      { onSettled: () => setPassword('') },
    );
  };
  const error = save.error ?? remove.error;

  return (
    <section className="mt-8 space-y-3" data-testid="registry-credentials">
      <div>
        <h4 className="text-sm font-semibold text-text-primary flex items-center gap-2">
          <KeyRound className="w-4 h-4" /> Private registries
        </h4>
        <p className="text-xs text-text-tertiary mt-1">
          A deploy pulls with the credential for its image's registry. Credentials are stored
          encrypted and are never shown again; replace or remove them here.
        </p>
      </div>
      {registries.length > 0 && (
        <ul className="bg-surface-card border border-border-primary rounded-lg divide-y divide-border-primary">
          {registries.map((name) => (
            <RegistryRow
              key={name}
              registry={name}
              removing={remove.isPending}
              onRemove={(r) => remove.mutate(r)}
            />
          ))}
        </ul>
      )}
      <form onSubmit={submit} className="grid gap-2 sm:grid-cols-4 items-end">
        <input
          aria-label="Registry"
          placeholder="ghcr.io"
          value={registry}
          onChange={(e) => setRegistry(e.target.value)}
          className={inputClass}
          data-testid="registry-host"
        />
        <input
          aria-label="Username"
          placeholder="Username"
          value={username}
          autoComplete="off"
          onChange={(e) => setUsername(e.target.value)}
          className={inputClass}
          data-testid="registry-username"
        />
        <input
          aria-label="Password or token"
          placeholder="Password or token"
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          className={inputClass}
          data-testid="registry-password"
        />
        <button
          type="submit"
          disabled={save.isPending || !registry.trim() || !username || !password}
          className={primaryButton}
          data-testid="registry-save"
        >
          Save
        </button>
      </form>
      {error && (
        <div role="alert" className="text-sm text-red-400">
          {apiErrorMessage(error, 'The credential could not be saved')}
        </div>
      )}
    </section>
  );
}
