import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Plus, Trash2, Loader2, Zap } from 'lucide-react';
import { useTriggers, useCreateTrigger, useDropTrigger, useTables, useFunctions } from '../hooks/useSchema';
import { SidePanel } from '../components/ui/SidePanel';
import { ConfirmModal } from '../components/ui/ConfirmModal';

interface TriggerForm {
  name: string;
  table: string;
  event: string;
  timing: string;
  function: string;
}

const EVENTS = ['INSERT', 'UPDATE', 'DELETE', 'TRUNCATE'];
const TIMINGS = ['BEFORE', 'AFTER', 'INSTEAD OF'];

export function TriggersPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: triggers = [], isLoading } = useTriggers(projectId ?? '');
  const { data: tables = [] } = useTables(projectId ?? '');
  const { data: functions = [] } = useFunctions(projectId ?? '');
  const createTrigger = useCreateTrigger(projectId ?? '');
  const dropTrigger = useDropTrigger(projectId ?? '');

  const [showCreate, setShowCreate] = useState(false);
  const [dropTarget, setDropTarget] = useState<{ name: string; table: string } | null>(null);
  const [form, setForm] = useState<TriggerForm>({ name: '', table: '', event: 'INSERT', timing: 'BEFORE', function: '' });

  const handleCreate = () => {
    if (!form.name.trim() || !form.table || !form.function) return;
    createTrigger.mutate(
      { name: form.name, table: form.table, event: form.event, timing: form.timing, function: form.function },
      { onSuccess: () => { setShowCreate(false); setForm({ name: '', table: '', event: 'INSERT', timing: 'BEFORE', function: '' }); } }
    );
  };

  // Group triggers by table
  const grouped = triggers.reduce<Record<string, typeof triggers>>((acc, t) => {
    const key = t.table;
    if (!acc[key]) acc[key] = [];
    acc[key].push(t);
    return acc;
  }, {});

  if (isLoading) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

  return (
    <div data-testid="triggers-page">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-lg font-semibold text-text-primary">Triggers</h3>
        <button
          onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 px-3 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg transition-colors"
          data-testid="create-trigger-btn"
        >
          <Plus className="w-4 h-4" /> Create Trigger
        </button>
      </div>

      {triggers.length === 0 ? (
        <div className="rounded-lg border border-border-primary bg-surface-card p-12 text-center text-text-tertiary text-sm">
          No triggers found
        </div>
      ) : (
        <div className="space-y-4">
          {Object.entries(grouped).map(([tableName, tableTriggers]) => (
            <div key={tableName} className="rounded-lg border border-border-primary bg-surface-card overflow-hidden">
              <div className="px-4 py-3 border-b border-border-primary flex items-center gap-2 bg-bg-secondary">
                <Zap className="w-4 h-4 text-purple-400" />
                <span className="text-sm font-medium text-text-primary">{tableName}</span>
                <span className="text-xs text-text-tertiary ml-auto">{tableTriggers.length} <span>{tableTriggers.length === 1 ? 'trigger' : 'triggers'}</span></span>
              </div>
              <table className="w-full text-sm">
                <thead>
                  <tr className="bg-surface-card">
                    <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Name</th>
                    <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Event</th>
                    <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Timing</th>
                    <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Function</th>
                    <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Enabled</th>
                    <th className="px-4 py-3 text-right text-xs font-medium text-text-secondary">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {tableTriggers.map((trigger) => (
                    <tr key={trigger.name} className="border-t border-border-primary hover:bg-surface-hover">
                      <td className="px-4 py-3 text-text-primary font-medium">{trigger.name}</td>
                      <td className="px-4 py-3 text-text-secondary">{trigger.event}</td>
                      <td className="px-4 py-3 text-text-secondary">{trigger.timing}</td>
                      <td className="px-4 py-3 text-text-secondary font-mono text-xs">{trigger.function}</td>
                      <td className="px-4 py-3">
                        {trigger.enabled
                          ? <span className="text-green-400 text-xs font-medium">enabled</span>
                          : <span className="text-text-tertiary text-xs">disabled</span>}
                      </td>
                      <td className="px-4 py-3 text-right">
                        <button
                          onClick={() => setDropTarget({ name: trigger.name, table: trigger.table })}
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
          ))}
        </div>
      )}

      <SidePanel
        open={showCreate}
        onClose={() => setShowCreate(false)}
        title="Create Trigger"
        footer={
          <button
            onClick={handleCreate}
            disabled={!form.name.trim() || !form.table || !form.function || createTrigger.isPending}
            className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg disabled:opacity-50 transition-colors"
          >
            {createTrigger.isPending ? 'Creating...' : 'Create Trigger'}
          </button>
        }
      >
        <div className="space-y-4">
          <div>
            <label htmlFor="trigger-name-input" className="block text-sm font-medium text-text-secondary mb-1">Trigger Name</label>
            <input id="trigger-name-input" type="text" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
              autoFocus />
          </div>
          <div>
            <label htmlFor="trigger-table-select" className="block text-sm font-medium text-text-secondary mb-1">Table</label>
            <select id="trigger-table-select" value={form.table} onChange={(e) => setForm({ ...form, table: e.target.value })}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500">
              <option value="">Select table...</option>
              {tables.map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
            </select>
          </div>
          <div>
            <label htmlFor="trigger-event-select" className="block text-sm font-medium text-text-secondary mb-1">Event</label>
            <select id="trigger-event-select" value={form.event} onChange={(e) => setForm({ ...form, event: e.target.value })}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500">
              {EVENTS.map((ev) => <option key={ev} value={ev}>{ev}</option>)}
            </select>
          </div>
          <div>
            <label htmlFor="trigger-timing-select" className="block text-sm font-medium text-text-secondary mb-1">Timing</label>
            <select id="trigger-timing-select" value={form.timing} onChange={(e) => setForm({ ...form, timing: e.target.value })}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500">
              {TIMINGS.map((t) => <option key={t} value={t}>{t}</option>)}
            </select>
          </div>
          <div>
            <label htmlFor="trigger-function-select" className="block text-sm font-medium text-text-secondary mb-1">Function</label>
            <select id="trigger-function-select" value={form.function} onChange={(e) => setForm({ ...form, function: e.target.value })}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500">
              <option value="">Select function...</option>
              {functions.map((f) => <option key={f.name} value={f.name}>{f.name}</option>)}
            </select>
          </div>
        </div>
      </SidePanel>

      <ConfirmModal
        open={!!dropTarget}
        onClose={() => setDropTarget(null)}
        onConfirm={() => {
          if (dropTarget) dropTrigger.mutate(dropTarget, { onSuccess: () => setDropTarget(null) });
        }}
        title="Drop Trigger"
        message={`Are you sure you want to drop the trigger "${dropTarget?.name}" on table "${dropTarget?.table}"? This cannot be undone.`}
        confirmLabel="Drop Trigger"
        destructive
        loading={dropTrigger.isPending}
      />
    </div>
  );
}
