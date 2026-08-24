import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Loader2, Trash2, Search, UserCheck, UserX } from 'lucide-react';
import { useAuthUsers, useUpdateAuthUser, useDeleteAuthUser } from '../hooks/useAuthUsers';
import { ConfirmModal } from '../components/ui/ConfirmModal';

export function AuthUsersPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: users = [], isLoading } = useAuthUsers(projectId || '');
  const updateUser = useUpdateAuthUser(projectId || '');
  const deleteUser = useDeleteAuthUser(projectId || '');
  const [search, setSearch] = useState('');
  const [deleteTarget, setDeleteTarget] = useState<{ id: number; email: string } | null>(null);

  const filtered = users.filter(u =>
    u.email.toLowerCase().includes(search.toLowerCase()) ||
    u.full_name.toLowerCase().includes(search.toLowerCase())
  );

  const toggleEnabled = (userId: number, current: boolean) => {
    updateUser.mutate({ userId, enabled: !current });
  };

  if (isLoading) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

  return (
    <div data-testid="auth-users-page">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-lg font-semibold text-text-primary">Auth Users</h3>
        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-text-tertiary" />
          <input
            type="text"
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search users..."
            className="pl-9 pr-4 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm w-64 focus:outline-none focus:ring-2 focus:ring-purple-500"
            data-testid="user-search"
          />
        </div>
      </div>

      <div className="rounded-lg border border-border-primary overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-surface-card">
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Email</th>
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Name</th>
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Role</th>
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Status</th>
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Last Login</th>
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Created</th>
              <th className="px-4 py-3 text-right text-xs font-medium text-text-secondary">Actions</th>
            </tr>
          </thead>
          <tbody>
            {filtered.map(user => (
              <tr key={user.id} className="border-t border-border-primary hover:bg-surface-hover" data-testid={`user-row-${user.id}`}>
                <td className="px-4 py-3 text-text-primary font-medium">{user.email}</td>
                <td className="px-4 py-3 text-text-secondary">{user.full_name}</td>
                <td className="px-4 py-3">
                  <span className="px-2 py-0.5 rounded text-xs bg-purple-500/10 text-purple-400 border border-purple-500/30">
                    {user.role}
                  </span>
                </td>
                <td className="px-4 py-3">
                  <button
                    onClick={() => toggleEnabled(user.id, user.enabled)}
                    className="flex items-center gap-1.5 text-xs"
                    disabled={updateUser.isPending}
                  >
                    {user.enabled ? (
                      <><UserCheck className="w-4 h-4 text-green-400" /><span className="text-green-400">Active</span></>
                    ) : (
                      <><UserX className="w-4 h-4 text-red-400" /><span className="text-red-400">Disabled</span></>
                    )}
                  </button>
                </td>
                <td className="px-4 py-3 text-text-tertiary text-xs">
                  {user.last_login_at ? new Date(user.last_login_at).toLocaleString() : 'Never'}
                </td>
                <td className="px-4 py-3 text-text-tertiary text-xs">
                  {new Date(user.created_at).toLocaleDateString()}
                </td>
                <td className="px-4 py-3 text-right">
                  <button onClick={() => setDeleteTarget({ id: user.id, email: user.email })}
                    className="p-1 text-text-tertiary hover:text-red-400 transition-colors">
                    <Trash2 className="w-4 h-4" />
                  </button>
                </td>
              </tr>
            ))}
            {filtered.length === 0 && (
              <tr><td colSpan={7} className="px-4 py-8 text-center text-text-tertiary text-sm">
                {search ? 'No users matching search' : 'No auth users registered yet'}
              </td></tr>
            )}
          </tbody>
        </table>
      </div>

      <ConfirmModal
        open={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => { if (deleteTarget) deleteUser.mutate(deleteTarget.id, { onSuccess: () => setDeleteTarget(null) }); }}
        title="Delete User"
        message={`Permanently delete "${deleteTarget?.email}"? This will remove all their data and sessions.`}
        confirmText={deleteTarget?.email}
        confirmLabel="Delete User"
        destructive
        loading={deleteUser.isPending}
      />
    </div>
  );
}
