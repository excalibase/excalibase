import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Plus, Trash2, Loader2, UserCheck, UserX } from 'lucide-react';
import { useRoles, useCreateRole, useDropRole } from '../hooks/useSchema';
import { SidePanel } from '../components/ui/SidePanel';
import { ConfirmModal } from '../components/ui/ConfirmModal';

export function RolesPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: roles = [], isLoading } = useRoles(projectId || '');
  const createRole = useCreateRole(projectId || '');
  const dropRole = useDropRole(projectId || '');

  const [showCreate, setShowCreate] = useState(false);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const [name, setName] = useState('');
  const [password, setPassword] = useState('');
  const [canLogin, setCanLogin] = useState(true);

  const handleCreate = () => {
    if (!name.trim()) return;
    createRole.mutate(
      { name, password: password || undefined, login: canLogin },
      { onSuccess: () => { setShowCreate(false); setName(''); setPassword(''); } }
    );
  };

  if (isLoading) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

  return (
    <div data-testid="roles-page">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-lg font-semibold text-text-primary">Database Roles</h3>
        <button
          onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 px-3 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg transition-colors"
          data-testid="create-role-btn"
        >
          <Plus className="w-4 h-4" /> Create Role
        </button>
      </div>

      <div className="rounded-lg border border-border-primary overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-surface-card">
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Name</th>
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Login</th>
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Superuser</th>
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Create DB</th>
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Create Role</th>
              <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Connections</th>
              <th className="px-4 py-3 text-right text-xs font-medium text-text-secondary">Actions</th>
            </tr>
          </thead>
          <tbody>
            {roles.map(role => (
              <tr key={role.name} className="border-t border-border-primary hover:bg-surface-hover" data-testid={`role-row-${role.name}`}>
                <td className="px-4 py-3 text-text-primary font-medium">{role.name}</td>
                <td className="px-4 py-3">{role.login ? <UserCheck className="w-4 h-4 text-green-400" /> : <UserX className="w-4 h-4 text-text-tertiary" />}</td>
                <td className="px-4 py-3">{role.superuser ? <span className="text-yellow-400 text-xs font-medium">YES</span> : <span className="text-text-tertiary text-xs">no</span>}</td>
                <td className="px-4 py-3">{role.createDb ? <span className="text-green-400 text-xs">yes</span> : <span className="text-text-tertiary text-xs">no</span>}</td>
                <td className="px-4 py-3">{role.createRole ? <span className="text-green-400 text-xs">yes</span> : <span className="text-text-tertiary text-xs">no</span>}</td>
                <td className="px-4 py-3 text-text-secondary text-xs">{role.connLimit === -1 ? 'unlimited' : String(role.connLimit)}</td>
                <td className="px-4 py-3 text-right">
                  {!role.superuser && (
                    <button
                      onClick={() => setDropTarget(role.name)}
                      className="p-1 text-text-tertiary hover:text-red-400 transition-colors"
                      data-testid={`drop-role-${role.name}`}
                    >
                      <Trash2 className="w-4 h-4" />
                    </button>
                  )}
                </td>
              </tr>
            ))}
            {roles.length === 0 && (
              <tr><td colSpan={7} className="px-4 py-8 text-center text-text-tertiary text-sm">No roles found</td></tr>
            )}
          </tbody>
        </table>
      </div>

      <SidePanel
        open={showCreate}
        onClose={() => setShowCreate(false)}
        title="Create Role"
        footer={
          <button onClick={handleCreate} disabled={!name.trim() || createRole.isPending}
            className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg disabled:opacity-50 transition-colors"
            data-testid="create-role-submit"
          >
            {createRole.isPending ? 'Creating...' : 'Create Role'}
          </button>
        }
      >
        <div className="space-y-4">
          <div>
            <label htmlFor="role-name-input" className="block text-sm font-medium text-text-secondary mb-1">Role Name</label>
            <input id="role-name-input" type="text" value={name} onChange={e => setName(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
              data-testid="role-name-input" autoFocus />
          </div>
          <div>
            <label htmlFor="role-password-input" className="block text-sm font-medium text-text-secondary mb-1">Password</label>
            <input id="role-password-input" type="password" value={password} onChange={e => setPassword(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
              data-testid="role-password-input" />
          </div>
          <label className="flex items-center gap-2 text-sm text-text-secondary">
            <input type="checkbox" checked={canLogin} onChange={e => setCanLogin(e.target.checked)} className="rounded" />
            {' '}Can Login
          </label>
        </div>
      </SidePanel>

      <ConfirmModal
        open={!!dropTarget}
        onClose={() => setDropTarget(null)}
        onConfirm={() => { if (dropTarget) dropRole.mutate(dropTarget, { onSuccess: () => setDropTarget(null) }); }}
        title="Drop Role"
        message={`Are you sure you want to drop the role "${dropTarget}"? This cannot be undone.`}
        confirmLabel="Drop Role"
        destructive
        loading={dropRole.isPending}
      />
    </div>
  );
}
