import { useState } from 'react';
import { useInstanceContext } from '../context/InstanceContext';
import { useMigrations, useApplyMigration, type MigrationRequest } from '../hooks/useMigrations';
import { CheckCircle, XCircle, Clock, ArrowUpDown, Plus, X } from 'lucide-react';

interface StatusBadgeProps {
  readonly status: string;
}

function StatusBadge({ status }: StatusBadgeProps) {
  if (status === 'APPLIED') return (
    <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs bg-green-900/20 text-green-400 border border-green-500/30">
      <CheckCircle className="w-3 h-3" /> Applied
    </span>
  );
  return (
    <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs bg-red-900/20 text-red-400 border border-red-500/30">
      <XCircle className="w-3 h-3" /> Failed
    </span>
  );
}

const DEFAULT_SQL = `-- Example: add a new table
CREATE TABLE IF NOT EXISTS orders (
  id SERIAL PRIMARY KEY,
  user_id INTEGER NOT NULL,
  total NUMERIC(10,2) NOT NULL,
  created_at TIMESTAMPTZ DEFAULT NOW()
);`;

export function MigrationsPage() {
  const { projectId } = useInstanceContext();

  const { data: migrations = [], isLoading } = useMigrations(projectId);
  const { mutate: apply, isPending, reset } = useApplyMigration(projectId);

  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState<MigrationRequest>({
    version: '',
    name: '',
    description: '',
    sql: DEFAULT_SQL,
  });
  const [toast, setToast] = useState<{ msg: string; ok: boolean } | null>(null);

  const nextVersion = `V${(migrations.length + 1)}`;

  function openForm() {
    setForm({ version: nextVersion, name: '', description: '', sql: DEFAULT_SQL });
    reset();
    setShowForm(true);
  }

  function submit() {
    if (!form.version.trim() || !form.name.trim() || !form.sql.trim()) return;
    apply(form, {
      onSuccess: (record) => {
        if (record.status === 'APPLIED') {
          setToast({ msg: `${record.version} applied in ${record.executionTimeMs}ms`, ok: true });
          setShowForm(false);
        } else {
          setToast({ msg: record.errorMessage ?? 'Migration failed', ok: false });
        }
        setTimeout(() => setToast(null), 5000);
      },
      onError: (e: unknown) => {
        const msg = e instanceof Error ? e.message : String(e);
        setToast({ msg: msg || 'Request failed', ok: false });
        setTimeout(() => setToast(null), 5000);
      },
    });
  }

  return (
    <div data-testid="migrations-page" className="max-w-5xl mx-auto space-y-6">
      <div className="flex justify-end">
        {projectId && (
          <button
            onClick={openForm}
            className="inline-flex items-center gap-2 px-4 py-2 bg-accent-primary hover:bg-accent-primary-hover text-white text-sm font-medium rounded-lg transition-colors"
          >
            <Plus className="w-4 h-4" /> New Migration
          </button>
        )}
      </div>

      {toast && (
        <div className={`p-3 rounded-lg text-sm border ${toast.ok ? 'bg-green-900/20 border-green-500/30 text-green-400' : 'bg-red-900/20 border-red-500/30 text-red-400'}`}>
          {toast.msg}
        </div>
      )}

      <div className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
        <div className="px-6 py-4 border-b border-border-primary flex items-center gap-2">
          <ArrowUpDown className="w-4 h-4 text-text-tertiary" />
          <span className="text-sm font-semibold text-text-primary">Migration History</span>
          <span className="ml-auto text-xs text-text-tertiary">{migrations.length} applied</span>
        </div>

        {isLoading && (
          <div className="p-12 text-center text-text-secondary text-sm">Loading…</div>
        )}
        {!isLoading && migrations.length === 0 && (
          <div className="p-12 text-center text-text-secondary text-sm">
            {projectId ? 'No migrations applied yet. Click "New Migration" to get started.' : 'Select an instance above.'}
          </div>
        )}
        {!isLoading && migrations.length > 0 && (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-text-tertiary border-b border-border-primary bg-bg-secondary text-left">
                <th className="px-6 py-3 font-medium">Version</th>
                <th className="px-6 py-3 font-medium">Name</th>
                <th className="px-6 py-3 font-medium">Status</th>
                <th className="px-6 py-3 font-medium">Applied At</th>
                <th className="px-6 py-3 font-medium">Duration</th>
                <th className="px-6 py-3 font-medium">Checksum</th>
              </tr>
            </thead>
            <tbody>
              {migrations.map((m) => (
                <tr key={m.id} className="border-b border-border-primary last:border-0 hover:bg-surface-hover transition-colors">
                  <td className="px-6 py-4 font-mono text-accent-primary font-semibold">{m.version}</td>
                  <td className="px-6 py-4 text-text-primary">
                    {m.name}
                    {m.description && <p className="text-text-tertiary text-xs mt-0.5">{m.description}</p>}
                  </td>
                  <td className="px-6 py-4"><StatusBadge status={m.status} /></td>
                  <td className="px-6 py-4 text-text-tertiary">{new Date(m.appliedAt).toLocaleString()}</td>
                  <td className="px-6 py-4 text-text-tertiary">
                    <span className="inline-flex items-center gap-1">
                      <Clock className="w-3 h-3" />{m.executionTimeMs}ms
                    </span>
                  </td>
                  <td className="px-6 py-4 font-mono text-text-tertiary text-xs">{m.checksum?.slice(0, 8)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {showForm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
          <div className="bg-surface-card border border-border-primary rounded-xl w-full max-w-2xl mx-4 shadow-2xl">
            <div className="flex items-center justify-between px-6 py-4 border-b border-border-primary">
              <h2 className="text-sm font-semibold text-text-primary">Apply Migration — {projectId}</h2>
              <button onClick={() => setShowForm(false)} className="text-text-tertiary hover:text-text-primary">
                <X className="w-4 h-4" />
              </button>
            </div>
            <div className="p-6 space-y-4">
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label htmlFor="migration-version" className="text-xs text-text-secondary mb-1 block">Version *</label>
                  <input
                    id="migration-version"
                    value={form.version}
                    onChange={(e) => setForm({ ...form, version: e.target.value })}
                    placeholder="e.g. V3"
                    className="w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-accent-primary"
                  />
                </div>
                <div>
                  <label htmlFor="migration-name" className="text-xs text-text-secondary mb-1 block">Name *</label>
                  <input
                    id="migration-name"
                    value={form.name}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                    placeholder="e.g. add_orders_table"
                    className="w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-accent-primary"
                  />
                </div>
              </div>
              <div>
                <label htmlFor="migration-desc" className="text-xs text-text-secondary mb-1 block">Description</label>
                <input
                  id="migration-desc"
                  value={form.description}
                  onChange={(e) => setForm({ ...form, description: e.target.value })}
                  placeholder="Optional description"
                  className="w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-accent-primary"
                />
              </div>
              <div>
                <label htmlFor="migration-sql" className="text-xs text-text-secondary mb-1 block">SQL *</label>
                <textarea
                  id="migration-sql"
                  value={form.sql}
                  onChange={(e) => setForm({ ...form, sql: e.target.value })}
                  rows={10}
                  className="w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm font-mono focus:outline-none focus:ring-2 focus:ring-accent-primary resize-none"
                />
              </div>
            </div>
            <div className="flex items-center justify-end gap-3 px-6 py-4 border-t border-border-primary">
              <button
                onClick={() => setShowForm(false)}
                className="px-4 py-2 text-text-secondary hover:text-text-primary text-sm transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={submit}
                disabled={isPending || !form.version.trim() || !form.name.trim() || !form.sql.trim()}
                className="px-4 py-2 bg-accent-primary hover:bg-accent-primary-hover disabled:opacity-50 text-white text-sm font-medium rounded-lg transition-colors"
              >
                {isPending ? 'Applying…' : 'Apply Migration'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
