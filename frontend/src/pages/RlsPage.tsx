import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Plus, Trash2, Loader2, Shield, ShieldCheck } from 'lucide-react';
import { usePolicies, useCreatePolicy, useDropPolicy, useTables, useUpdateTable } from '../hooks/useSchema';
import { SidePanel } from '../components/ui/SidePanel';
import { ConfirmModal } from '../components/ui/ConfirmModal';

export function RlsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: policies = [], isLoading } = usePolicies(projectId || '');
  const { data: tables = [] } = useTables(projectId || '');
  const createPolicy = useCreatePolicy(projectId || '');
  const dropPolicy = useDropPolicy(projectId || '');
  const updateTable = useUpdateTable(projectId || '');

  const [showCreate, setShowCreate] = useState(false);
  const [dropTarget, setDropTarget] = useState<{ table: string; name: string } | null>(null);

  // Form state
  const [pName, setPName] = useState('');
  const [pTable, setPTable] = useState('');
  const [pCommand, setPCommand] = useState('ALL');
  const [pRoles, setPRoles] = useState('public');
  const [pUsing, setPUsing] = useState('');
  const [pWithCheck, setPWithCheck] = useState('');

  const handleCreate = () => {
    if (!pName.trim() || !pTable) return;
    createPolicy.mutate(
      { name: pName, table: pTable, command: pCommand, roles: pRoles, using: pUsing || undefined, withCheck: pWithCheck || undefined },
      { onSuccess: () => { setShowCreate(false); setPName(''); setPUsing(''); setPWithCheck(''); } }
    );
  };

  // Group policies by table
  const grouped = policies.reduce<Record<string, typeof policies>>((acc, p) => {
    if (!acc[p.table]) acc[p.table] = [];
    acc[p.table].push(p);
    return acc;
  }, {});

  if (isLoading) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

  return (
    <div data-testid="rls-page">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-lg font-semibold text-text-primary">Row-Level Security</h3>
        <button
          onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 px-3 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg transition-colors"
          data-testid="create-policy-btn"
        >
          <Plus className="w-4 h-4" /> Create Policy
        </button>
      </div>

      {/* RLS toggle per table */}
      <div className="mb-6 space-y-2">
        <h4 className="text-sm font-medium text-text-secondary">Enable RLS per table</h4>
        <div className="flex flex-wrap gap-2">
          {tables.filter(t => t.type === 'BASE TABLE').map(t => (
            <button
              key={t.name}
              onClick={() => updateTable.mutate({ tableName: t.name, rlsEnabled: true })}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-border-primary text-xs font-medium text-text-secondary hover:bg-surface-hover transition-colors"
              data-testid={`rls-toggle-${t.name}`}
            >
              <Shield className="w-3.5 h-3.5" /> {t.name}
            </button>
          ))}
        </div>
      </div>

      {/* Policies grouped by table */}
      {Object.keys(grouped).length === 0 ? (
        <div className="text-center py-12 text-text-tertiary text-sm">
          <ShieldCheck className="w-10 h-10 mx-auto mb-2 text-text-tertiary" />
          No policies defined yet
        </div>
      ) : (
        Object.entries(grouped).map(([table, tablePolicies]) => (
          <div key={table} className="mb-6">
            <h4 className="text-sm font-medium text-text-secondary mb-2 flex items-center gap-2">
              <Shield className="w-4 h-4 text-purple-400" /> {table}
            </h4>
            <div className="rounded-lg border border-border-primary overflow-hidden">
              <table className="w-full text-sm">
                <thead>
                  <tr className="bg-surface-card">
                    <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Policy</th>
                    <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Command</th>
                    <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Roles</th>
                    <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">USING</th>
                    <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">WITH CHECK</th>
                    <th className="px-4 py-2 text-right text-xs font-medium text-text-secondary">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {tablePolicies.map(p => (
                    <tr key={p.name} className="border-t border-border-primary hover:bg-surface-hover" data-testid={`policy-row-${p.name}`}>
                      <td className="px-4 py-2 text-text-primary font-medium">{p.name}</td>
                      <td className="px-4 py-2 text-text-secondary text-xs">{p.command}</td>
                      <td className="px-4 py-2 text-text-secondary text-xs">{p.roles}</td>
                      <td className="px-4 py-2 text-text-tertiary text-xs font-mono max-w-48 truncate">{p.using || '-'}</td>
                      <td className="px-4 py-2 text-text-tertiary text-xs font-mono max-w-48 truncate">{p.withCheck || '-'}</td>
                      <td className="px-4 py-2 text-right">
                        <button
                          onClick={() => setDropTarget({ table, name: p.name })}
                          className="p-1 text-text-tertiary hover:text-red-400 transition-colors"
                        >
                          <Trash2 className="w-4 h-4" />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        ))
      )}

      <SidePanel
        open={showCreate}
        onClose={() => setShowCreate(false)}
        title="Create Policy"
        footer={
          <button onClick={handleCreate} disabled={!pName.trim() || !pTable || createPolicy.isPending}
            className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg disabled:opacity-50 transition-colors"
            data-testid="create-policy-submit"
          >
            {createPolicy.isPending ? 'Creating...' : 'Create Policy'}
          </button>
        }
      >
        <div className="space-y-4">
          <div>
            <label htmlFor="policy-name-input" className="block text-sm font-medium text-text-secondary mb-1">Policy Name</label>
            <input id="policy-name-input" type="text" value={pName} onChange={e => setPName(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
              data-testid="policy-name-input" autoFocus />
          </div>
          <div>
            <label htmlFor="policy-table-select" className="block text-sm font-medium text-text-secondary mb-1">Table</label>
            <select id="policy-table-select" value={pTable} onChange={e => setPTable(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500">
              <option value="">Select table...</option>
              {tables.filter(t => t.type === 'BASE TABLE').map(t => (
                <option key={t.name} value={t.name}>{t.name}</option>
              ))}
            </select>
          </div>
          <div>
            <label htmlFor="policy-command-select" className="block text-sm font-medium text-text-secondary mb-1">Command</label>
            <select id="policy-command-select" value={pCommand} onChange={e => setPCommand(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500">
              {['ALL', 'SELECT', 'INSERT', 'UPDATE', 'DELETE'].map(c => <option key={c} value={c}>{c}</option>)}
            </select>
          </div>
          <div>
            <label htmlFor="policy-roles-input" className="block text-sm font-medium text-text-secondary mb-1">Roles</label>
            <input id="policy-roles-input" type="text" value={pRoles} onChange={e => setPRoles(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500" />
          </div>
          <div>
            <label htmlFor="policy-using-input" className="block text-sm font-medium text-text-secondary mb-1">USING expression</label>
            <textarea id="policy-using-input" value={pUsing} onChange={e => setPUsing(e.target.value)} rows={3}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm font-mono focus:outline-none focus:ring-2 focus:ring-purple-500"
              placeholder="e.g. auth.uid() = user_id" />
          </div>
          <div>
            <label htmlFor="policy-withcheck-input" className="block text-sm font-medium text-text-secondary mb-1">WITH CHECK expression</label>
            <textarea id="policy-withcheck-input" value={pWithCheck} onChange={e => setPWithCheck(e.target.value)} rows={3}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm font-mono focus:outline-none focus:ring-2 focus:ring-purple-500"
              placeholder="e.g. auth.uid() = user_id" />
          </div>
        </div>
      </SidePanel>

      <ConfirmModal
        open={!!dropTarget}
        onClose={() => setDropTarget(null)}
        onConfirm={() => { if (dropTarget) dropPolicy.mutate(dropTarget, { onSuccess: () => setDropTarget(null) }); }}
        title="Drop Policy"
        message={`Are you sure you want to drop the policy "${dropTarget?.name}" from "${dropTarget?.table}"?`}
        confirmLabel="Drop Policy"
        destructive
        loading={dropPolicy.isPending}
      />
    </div>
  );
}
