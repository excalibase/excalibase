import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Check, Copy, Loader2, UserCog } from 'lucide-react';
import { Button } from '../components/Button';
import { useProjectIsDocumentDB } from '../hooks/useDocuments';
import { useProjectEndpoint, type ProjectEndpoint } from '../api/projectEndpoint';
import {
  MONGO_USERNAME_PATTERN,
  ROLE_LABELS,
  mongoUserUri,
  useCreateMongoUser,
  useDeleteMongoUser,
  useMongoUsers,
  useRotateMongoUser,
  type MongoUserCredential,
  type MongoUserRole,
} from '../api/mongoUsers';

function errorText(err: unknown, fallback: string): string {
  const axiosErr = err as { response?: { data?: { error?: string } } };
  return axiosErr?.response?.data?.error || fallback;
}

// connectionStrings are the Mongo addresses this credential works on: the
// in-cluster one always, the public one only while it is answering.
function connectionStrings(
  cred: MongoUserCredential,
  endpoint?: ProjectEndpoint,
): { label: string; uri: string }[] {
  const strings: { label: string; uri: string }[] = [];
  const internal = endpoint?.mongo?.internal;
  if (internal) {
    strings.push({
      label: 'Internal',
      uri: mongoUserUri(cred.username, cred.password, internal.host, internal.port, true),
    });
  }
  const mongo = endpoint?.mongo;
  if (endpoint?.publicEnabled && endpoint.available && mongo?.available && mongo.port) {
    strings.push({
      label: 'Public',
      uri: mongoUserUri(
        cred.username,
        cred.password,
        endpoint.host,
        mongo.port,
        // The gateway takes TLS only; Require TLS changes Postgres alone (EXC-530).
        true,
      ),
    });
  }
  return strings;
}

function CopyButton({ value, label }: { readonly value: string; readonly label: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard?.writeText(value);
    setCopied(true);
  };
  return (
    <button
      type="button"
      onClick={copy}
      aria-label={label}
      className="p-2 text-text-secondary hover:text-text-primary"
    >
      {copied ? <Check className="w-4 h-4" /> : <Copy className="w-4 h-4" />}
    </button>
  );
}

function NewCredentialNotice({
  cred,
  endpoint,
  onDone,
}: {
  readonly cred: MongoUserCredential;
  readonly endpoint?: ProjectEndpoint;
  readonly onDone: () => void;
}) {
  return (
    <div
      data-testid="new-mongo-user"
      className="space-y-3 p-4 rounded-lg border border-purple-500/30 bg-purple-500/10"
    >
      <p className="text-sm text-text-primary">
        Copy the password for <strong>{cred.username}</strong> now. It will not be shown again.
      </p>
      <div className="flex items-center gap-2">
        <code className="flex-1 px-3 py-2 rounded bg-bg-secondary text-text-primary text-xs break-all">
          {cred.password}
        </code>
        <CopyButton value={cred.password} label="Copy password" />
      </div>
      {connectionStrings(cred, endpoint).map(({ label, uri }) => (
        <div key={label}>
          <p className="text-xs text-text-secondary mb-1">{label}</p>
          <div className="flex items-center gap-2">
            <code className="flex-1 px-3 py-2 rounded bg-bg-secondary text-text-primary text-xs break-all">
              {uri}
            </code>
            <CopyButton value={uri} label={`Copy ${label.toLowerCase()} connection string`} />
          </div>
        </div>
      ))}
      <Button type="button" onClick={onDone}>
        I have saved it
      </Button>
    </div>
  );
}

export function MongoUsersPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const documentDbQuery = useProjectIsDocumentDB(projectId);
  const isDocumentDB = documentDbQuery.data === true;
  const users = useMongoUsers(projectId, isDocumentDB);
  const endpoint = useProjectEndpoint(isDocumentDB ? projectId : undefined);
  const createUser = useCreateMongoUser(projectId);
  const rotateUser = useRotateMongoUser(projectId);
  const deleteUser = useDeleteMongoUser(projectId);
  const [username, setUsername] = useState('');
  const [role, setRole] = useState<MongoUserRole>('read');
  const [shown, setShown] = useState<MongoUserCredential | null>(null);
  const [error, setError] = useState<string | null>(null);

  if (documentDbQuery.isLoading)
    return <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" />;
  if (!isDocumentDB) {
    return (
      <p className="text-sm text-text-secondary" data-testid="mongo-users-unavailable">
        Mongo users are available for projects created with DocumentDB.
      </p>
    );
  }

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    const name = username.trim();
    if (!MONGO_USERNAME_PATTERN.test(name)) {
      setError('Use 3-63 lowercase letters, digits or underscores, starting with a letter.');
      return;
    }
    try {
      setShown(await createUser.mutateAsync({ username: name, role }));
      setUsername('');
    } catch (err) {
      setError(errorText(err, 'Could not create the user'));
    }
  };

  const rotate = async (name: string) => {
    if (!window.confirm(`Give "${name}" a new password? New logins with the old one stop working.`))
      return;
    setError(null);
    try {
      setShown(await rotateUser.mutateAsync(name));
    } catch (err) {
      setError(errorText(err, 'Could not rotate the password'));
    }
  };

  const remove = async (name: string) => {
    if (
      !window.confirm(`Delete "${name}"? Its connections are closed and it can no longer log in.`)
    )
      return;
    setError(null);
    try {
      await deleteUser.mutateAsync(name);
      if (shown?.username === name) setShown(null);
    } catch (err) {
      setError(errorText(err, 'Could not delete the user'));
    }
  };

  const list = users.data?.users ?? [];
  return (
    <div className="space-y-6 max-w-4xl" data-testid="mongo-users-page">
      <div>
        <h3 className="flex items-center gap-2 text-lg font-semibold text-text-primary">
          <UserCog className="w-5 h-5" /> Mongo Users
        </h3>
        <p className="text-sm text-text-secondary mt-1">
          Extra logins for Mongo drivers and tools, one per application or service. They connect
          over the MongoDB protocol only and cannot log in with psql or any SQL client. A restored
          project starts with none of the source project&apos;s users: create them again.
        </p>
      </div>

      {error && (
        <div
          role="alert"
          className="px-4 py-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm"
        >
          {error}
        </div>
      )}
      {shown && (
        <NewCredentialNotice cred={shown} endpoint={endpoint.data} onDone={() => setShown(null)} />
      )}

      <form onSubmit={create} className="flex flex-wrap items-end gap-3">
        <div>
          <label htmlFor="mongo-user-name" className="block text-sm text-text-secondary mb-1">
            User name
          </label>
          <input
            id="mongo-user-name"
            value={username}
            maxLength={63}
            onChange={(e) => setUsername(e.target.value)}
            className="px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary"
            placeholder="reporting"
          />
        </div>
        <div>
          <label htmlFor="mongo-user-role" className="block text-sm text-text-secondary mb-1">
            Role
          </label>
          <select
            id="mongo-user-role"
            value={role}
            onChange={(e) => setRole(e.target.value as MongoUserRole)}
            className="px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary"
          >
            <option value="read">{ROLE_LABELS.read}</option>
            <option value="readWrite">{ROLE_LABELS.readWrite}</option>
          </select>
        </div>
        <Button type="submit" disabled={createUser.isPending} className="flex items-center gap-2">
          {createUser.isPending && <Loader2 className="w-4 h-4 animate-spin" />}
          Create user
        </Button>
      </form>
      <p className="text-xs text-text-tertiary">
        3–63 lowercase letters, digits and underscores, starting with a letter.
      </p>

      {users.isLoading && <Loader2 className="w-5 h-5 animate-spin text-text-secondary" />}
      {users.isError && (
        <p className="text-sm text-red-400">{errorText(users.error, 'Could not load users')}</p>
      )}
      {users.data && (
        <p className="text-xs text-text-tertiary">
          {list.length} of {users.data.limit} users
        </p>
      )}
      {users.data && list.length === 0 && (
        <p className="text-sm text-text-secondary">No Mongo users yet.</p>
      )}
      {list.length > 0 && (
        <table className="w-full text-sm">
          <thead className="text-left text-text-secondary">
            <tr>
              <th className="py-2">User</th>
              <th>Role</th>
              <th>Created</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {list.map((user) => (
              <tr
                key={user.username}
                data-testid={`mongo-user-${user.username}`}
                className="border-t border-border-primary text-text-primary"
              >
                <td className="py-2">
                  <code className="text-xs">{user.username}</code>
                </td>
                <td>{ROLE_LABELS[user.role] ?? user.role}</td>
                <td>{user.createdAt ? new Date(user.createdAt).toLocaleString() : '—'}</td>
                <td className="text-right space-x-3">
                  <button
                    type="button"
                    onClick={() => rotate(user.username)}
                    className="text-text-secondary hover:text-text-primary"
                  >
                    Rotate password
                  </button>
                  <button
                    type="button"
                    onClick={() => remove(user.username)}
                    className="text-red-400 hover:text-red-300"
                  >
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
