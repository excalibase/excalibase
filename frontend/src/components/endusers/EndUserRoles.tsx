import { useState } from 'react';
import { useMutation, useQuery, useQueryClient, type UseQueryResult } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Loader2 } from 'lucide-react';
import { listEndUsers, setEndUserRole, type EndUser, type EndUserPage, type EndUserRoleChange } from '../../api/endUsers';
import { apiErrorMessage } from '../../api/permissions';
import { useProjectRole } from '../../hooks/useProjectRole';
import { validateEndUserRole } from '../../utils/permissionRules';

// The role each end user's token carries (EXC-370): the API permissions of
// that role decide what the user reaches. Admin and up only, as the route is.

const PAGE_LIMIT = 100;
const MAX_ALLOWED_ROLES = 32;

const endUsersKey = (projectId: string) => ['end-users', projectId] as const;

function parseRoleChange(role: string, allowedText: string): { change?: EndUserRoleChange; error?: string } {
  const roleError = validateEndUserRole(role);
  if (roleError) return { error: roleError };
  const allowed = allowedText.split(',').map((r) => r.trim()).filter(Boolean);
  if (allowed.length === 0) return { change: { role } };
  for (const candidate of allowed) {
    const problem = validateEndUserRole(candidate);
    if (problem) return { error: problem };
  }
  if (new Set(allowed).size !== allowed.length) return { error: 'A role is listed twice in the allowed roles.' };
  if (allowed.length > MAX_ALLOWED_ROLES) return { error: `At most ${MAX_ALLOWED_ROLES} allowed roles.` };
  if (!allowed.includes(role)) return { error: `The allowed roles must include ${role}.` };
  return { change: { role, allowedRoles: allowed } };
}

export function EndUserRoles({ projectId }: { readonly projectId: string }) {
  const { canAdmin, isLoading: roleLoading } = useProjectRole(projectId);
  const users = useQuery({
    queryKey: endUsersKey(projectId),
    queryFn: () => listEndUsers(projectId, { limit: PAGE_LIMIT }),
    enabled: !!projectId && canAdmin,
  });

  return (
    <section className="mt-8 space-y-3" data-testid="end-user-roles">
      <div>
        <h3 className="text-lg font-semibold text-text-primary">API roles</h3>
        <p className="text-xs text-text-tertiary mt-1">
          The role an end user's requests run as, and the roles they may switch to with <code>X-Excalibase-Role</code>.
          A table's permissions for that role decide what they reach. Changing a role signs the user out of their
          existing sessions.
        </p>
      </div>
      <EndUserRolesBody roleLoading={roleLoading} canAdmin={canAdmin} users={users}>
        {(list) => <EndUserTable projectId={projectId} users={list} total={users.data?.total} />}
      </EndUserRolesBody>
    </section>
  );
}

interface BodyProps {
  readonly roleLoading: boolean;
  readonly canAdmin: boolean;
  readonly users: UseQueryResult<EndUserPage>;
  readonly children: (users: EndUser[]) => React.ReactNode;
}

function EndUserRolesBody({ roleLoading, canAdmin, users, children }: BodyProps) {
  if (roleLoading || (canAdmin && users.isLoading)) {
    return <Loader2 className="w-5 h-5 animate-spin text-purple-400" />;
  }
  if (!canAdmin) {
    return <p className="text-sm text-text-tertiary">Only project admins and owners can see and change end-user roles.</p>;
  }
  if (users.error) {
    return (
      <p role="alert" className="text-sm text-red-400">
        {apiErrorMessage(users.error, 'End users could not be listed.')}
      </p>
    );
  }
  const list = users.data?.users ?? [];
  if (list.length === 0) return <p className="text-sm text-text-tertiary">No end users yet.</p>;
  return <>{children(list)}</>;
}

function EndUserTable({ projectId, users, total }: Readonly<{ projectId: string; users: EndUser[]; total?: number }>) {
  return (
    <div className="rounded-lg border border-border-primary overflow-hidden">
      <table className="w-full text-sm">
        <thead>
          <tr className="bg-surface-card">
            <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Email</th>
            <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Role</th>
            <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Allowed roles</th>
            <th className="px-4 py-3 text-right text-xs font-medium text-text-secondary">Actions</th>
          </tr>
        </thead>
        <tbody>
          {users.map((user) => (
            <EndUserRow key={user.id} projectId={projectId} user={user} />
          ))}
        </tbody>
      </table>
      {total !== undefined && total > users.length && (
        <p className="px-4 py-2 text-xs text-text-tertiary border-t border-border-primary">
          Showing the first {users.length} of {total} end users.
        </p>
      )}
    </div>
  );
}

function EndUserRow({ projectId, user }: Readonly<{ projectId: string; user: EndUser }>) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [role, setRole] = useState(user.role);
  const [allowed, setAllowed] = useState(user.allowedRoles.join(', '));
  const [error, setError] = useState<string | null>(null);
  const save = useMutation({
    mutationFn: (change: EndUserRoleChange) => setEndUserRole(projectId, user.id, change),
    onSuccess: () => {
      toast.success(`${user.email} now runs as ${role}; their sessions were signed out`);
      setEditing(false);
      qc.invalidateQueries({ queryKey: endUsersKey(projectId) });
    },
    onError: (err) => setError(apiErrorMessage(err, 'The role could not be changed.')),
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const parsed = parseRoleChange(role.trim(), allowed);
    setError(parsed.error ?? null);
    if (parsed.change) save.mutate(parsed.change);
  };

  const inputClass =
    'w-full px-2 py-1 rounded border border-border-primary bg-bg-primary text-text-primary text-xs font-mono';

  return (
    <tr className="border-t border-border-primary align-top" data-testid={`end-user-${user.id}`}>
      <td className="px-4 py-3 text-text-primary">{user.email}</td>
      {editing ? (
        <td colSpan={3} className="px-4 py-3">
          <form onSubmit={submit} className="space-y-2">
            <div className="grid grid-cols-2 gap-2">
              <input aria-label={`Role for ${user.email}`} value={role} onChange={(e) => setRole(e.target.value)} className={inputClass} />
              <input
                aria-label={`Allowed roles for ${user.email}`}
                value={allowed}
                onChange={(e) => setAllowed(e.target.value)}
                placeholder="comma-separated, empty for the role only"
                className={inputClass}
              />
            </div>
            {error && (
              <p role="alert" className="text-xs text-red-400">
                {error}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <button type="button" onClick={() => { setEditing(false); setError(null); }} className="px-2 py-1 text-xs rounded text-text-tertiary hover:bg-surface-hover">
                Cancel
              </button>
              <button type="submit" disabled={save.isPending} className="px-3 py-1 text-xs rounded bg-purple-500 hover:bg-purple-600 text-white disabled:opacity-50">
                {save.isPending ? 'Saving...' : 'Save'}
              </button>
            </div>
          </form>
        </td>
      ) : (
        <>
          <td className="px-4 py-3">
            <span className="px-2 py-0.5 rounded text-xs bg-purple-500/10 text-purple-400 border border-purple-500/30 font-mono">
              {user.role}
            </span>
          </td>
          <td className="px-4 py-3 text-xs text-text-secondary font-mono">{user.allowedRoles.join(', ')}</td>
          <td className="px-4 py-3 text-right">
            <button
              type="button"
              aria-label={`Change role of ${user.email}`}
              onClick={() => setEditing(true)}
              className="px-2 py-1 text-xs rounded text-purple-400 hover:bg-purple-500/10"
            >
              Change role
            </button>
          </td>
        </>
      )}
    </tr>
  );
}
