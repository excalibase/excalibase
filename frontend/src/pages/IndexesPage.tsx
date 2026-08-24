import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Plus, Trash2, Loader2, List } from 'lucide-react';
import { useTables, useIndexes, useCreateIndex, useDropIndex, useColumns } from '../hooks/useSchema';
import { SidePanel } from '../components/ui/SidePanel';
import { ConfirmModal } from '../components/ui/ConfirmModal';

interface IndexForm {
  name: string;
  table: string;
  columns: string[];
  unique: boolean;
  type: string;
}

const INDEX_TYPES = ['btree', 'hash', 'gin', 'gist', 'brin'];

export function IndexesPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: tables = [] } = useTables(projectId ?? '');

  const [selectedTable, setSelectedTable] = useState('');
  const { data: indexes = [], isLoading } = useIndexes(projectId ?? '', selectedTable);
  const { data: columns = [] } = useColumns(projectId ?? '', selectedTable);
  const createIndex = useCreateIndex(projectId ?? '');
  const dropIndex = useDropIndex(projectId ?? '');

  const [showCreate, setShowCreate] = useState(false);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const [form, setForm] = useState<IndexForm>({ name: '', table: '', columns: [], unique: false, type: 'btree' });

  // When opening the create panel, pre-fill table if one is selected
  const openCreate = () => {
    setForm({ name: '', table: selectedTable, columns: [], unique: false, type: 'btree' });
    setShowCreate(true);
  };

  const toggleColumn = (col: string) => {
    setForm((prev) => ({
      ...prev,
      columns: prev.columns.includes(col)
        ? prev.columns.filter((c) => c !== col)
        : [...prev.columns, col],
    }));
  };

  const handleCreate = () => {
    if (!form.name.trim() || !form.table || form.columns.length === 0) return;
    createIndex.mutate(
      { name: form.name, table: form.table, columns: form.columns, unique: form.unique, type: form.type },
      { onSuccess: () => { setShowCreate(false); setForm({ name: '', table: '', columns: [], unique: false, type: 'btree' }); } }
    );
  };

  return (
    <div data-testid="indexes-page">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-lg font-semibold text-text-primary">Indexes</h3>
        <button
          onClick={openCreate}
          className="flex items-center gap-2 px-3 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg transition-colors"
          data-testid="create-index-btn"
        >
          <Plus className="w-4 h-4" /> Create Index
        </button>
      </div>

      {/* Table selector */}
      <div className="mb-4">
        <select
          value={selectedTable}
          onChange={(e) => setSelectedTable(e.target.value)}
          className="px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
          data-testid="table-selector"
        >
          <option value="">Select a table...</option>
          {tables.map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
        </select>
      </div>

      {selectedTable === '' && (
        <div className="rounded-lg border border-border-primary bg-surface-card p-12 text-center text-text-tertiary text-sm">
          Select a table to view its indexes
        </div>
      )}
      {selectedTable !== '' && isLoading && (
        <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>
      )}
      {selectedTable !== '' && !isLoading && (
        <div className="rounded-lg border border-border-primary bg-surface-card overflow-hidden">
          <div className="px-4 py-3 border-b border-border-primary flex items-center gap-2 bg-bg-secondary">
            <List className="w-4 h-4 text-purple-400" />
            <span className="text-sm font-medium text-text-primary">Indexes on {selectedTable}</span>
            <span className="text-xs text-text-tertiary ml-auto">{indexes.length} <span>{indexes.length === 1 ? 'index' : 'indexes'}</span></span>
          </div>
          {indexes.length === 0 && (
            <div className="p-8 text-center text-text-tertiary text-sm">No indexes on this table</div>
          )}
          {indexes.length > 0 && (
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-surface-card">
                  <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Name</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Columns</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Unique</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Type</th>
                  <th className="px-4 py-3 text-right text-xs font-medium text-text-secondary">Actions</th>
                </tr>
              </thead>
              <tbody>
                {indexes.map((idx) => (
                  <tr key={idx.name} className="border-t border-border-primary hover:bg-surface-hover">
                    <td className="px-4 py-3 text-text-primary font-medium">{idx.name}</td>
                    <td className="px-4 py-3 text-text-secondary font-mono text-xs">{idx.columns.join(', ')}</td>
                    <td className="px-4 py-3">
                      {idx.unique
                        ? <span className="text-green-400 text-xs font-medium">yes</span>
                        : <span className="text-text-tertiary text-xs">no</span>}
                    </td>
                    <td className="px-4 py-3 text-text-secondary text-xs">{idx.type}</td>
                    <td className="px-4 py-3 text-right">
                      <button
                        onClick={() => setDropTarget(idx.name)}
                        className="p-1 text-text-tertiary hover:text-red-400 transition-colors"
                      >
                        <Trash2 className="w-4 h-4" />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      <SidePanel
        open={showCreate}
        onClose={() => setShowCreate(false)}
        title="Create Index"
        footer={
          <button
            onClick={handleCreate}
            disabled={!form.name.trim() || !form.table || form.columns.length === 0 || createIndex.isPending}
            className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg disabled:opacity-50 transition-colors"
          >
            {createIndex.isPending ? 'Creating...' : 'Create Index'}
          </button>
        }
      >
        <div className="space-y-4">
          <div>
            <label htmlFor="index-name-input" className="block text-sm font-medium text-text-secondary mb-1">Index Name</label>
            <input id="index-name-input" type="text" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
              autoFocus />
          </div>
          <div>
            <label htmlFor="index-table-select" className="block text-sm font-medium text-text-secondary mb-1">Table</label>
            <select id="index-table-select" value={form.table} onChange={(e) => setForm({ ...form, table: e.target.value, columns: [] })}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500">
              <option value="">Select table...</option>
              {tables.map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
            </select>
          </div>
          {form.table && (
            <div>
              <span className="block text-sm font-medium text-text-secondary mb-1">Columns</span>
              <div className="space-y-2">
                {columns.map((col) => (
                  <label key={col.name} className="flex items-center gap-2 text-sm text-text-secondary">
                    <input
                      type="checkbox"
                      checked={form.columns.includes(col.name)}
                      onChange={() => toggleColumn(col.name)}
                      className="rounded"
                    />
                    {col.name} <span className="text-text-tertiary text-xs">({col.dataType})</span>
                  </label>
                ))}
              </div>
            </div>
          )}
          <label className="flex items-center gap-2 text-sm text-text-secondary">
            <input type="checkbox" checked={form.unique} onChange={(e) => setForm({ ...form, unique: e.target.checked })} className="rounded" />
            {' '}Unique
          </label>
          <div>
            <label htmlFor="index-type-select" className="block text-sm font-medium text-text-secondary mb-1">Type</label>
            <select id="index-type-select" value={form.type} onChange={(e) => setForm({ ...form, type: e.target.value })}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500">
              {INDEX_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
            </select>
          </div>
        </div>
      </SidePanel>

      <ConfirmModal
        open={!!dropTarget}
        onClose={() => setDropTarget(null)}
        onConfirm={() => {
          if (dropTarget) dropIndex.mutate({ name: dropTarget }, { onSuccess: () => setDropTarget(null) });
        }}
        title="Drop Index"
        message={`Are you sure you want to drop the index "${dropTarget}"? This cannot be undone.`}
        confirmLabel="Drop Index"
        destructive
        loading={dropIndex.isPending}
      />
    </div>
  );
}
