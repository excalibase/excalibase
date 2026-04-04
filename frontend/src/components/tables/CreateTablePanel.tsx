import { useState } from 'react';
import { Trash2 } from 'lucide-react';
import { SidePanel } from '../ui/SidePanel';
import { useCreateTable } from '../../hooks/useSchema';

interface ColumnDraft {
  name: string;
  type: string;
  primaryKey: boolean;
  nullable: boolean;
  unique: boolean;
}

const DEFAULT_COLUMNS: ColumnDraft[] = [
  { name: 'id', type: 'serial', primaryKey: true, nullable: false, unique: false },
];

interface CreateTablePanelProps {
  open: boolean;
  onClose: () => void;
  projectId: string;
}

export function CreateTablePanel({ open, onClose, projectId }: CreateTablePanelProps) {
  const createTable = useCreateTable(projectId);
  const [newTableName, setNewTableName] = useState('');
  const [newCols, setNewCols] = useState<ColumnDraft[]>([...DEFAULT_COLUMNS]);

  const handleCreate = () => {
    if (!newTableName.trim()) return;
    createTable.mutate(
      { name: newTableName, columns: newCols.map(c => ({ ...c, default: undefined })) },
      {
        onSuccess: () => {
          onClose();
          setNewTableName('');
          setNewCols([...DEFAULT_COLUMNS]);
        },
      },
    );
  };

  const handleClose = () => {
    onClose();
    setNewTableName('');
    setNewCols([...DEFAULT_COLUMNS]);
  };

  return (
    <SidePanel
      open={open}
      onClose={handleClose}
      title="Create Table"
      footer={
        <button
          onClick={handleCreate}
          disabled={!newTableName.trim() || createTable.isPending}
          className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg disabled:opacity-50"
          data-testid="create-table-submit"
        >
          {createTable.isPending ? 'Creating...' : 'Create Table'}
        </button>
      }
    >
      <div className="space-y-4">
        <div>
          <label className="block text-sm font-medium text-text-secondary mb-1">Table Name</label>
          <input
            type="text"
            value={newTableName}
            onChange={e => setNewTableName(e.target.value)}
            className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
            placeholder="e.g. users"
            data-testid="table-name-input"
            autoFocus
          />
        </div>
        <div>
          <label className="block text-sm font-medium text-text-secondary mb-1">Columns</label>
          {newCols.map((col, i) => (
            <div key={`newcol-${i}`} className="flex gap-2 mb-2">
              <input
                value={col.name}
                onChange={e => {
                  const c = [...newCols];
                  c[i] = { ...c[i], name: e.target.value };
                  setNewCols(c);
                }}
                className="flex-1 px-2 py-1.5 rounded border border-border-primary bg-bg-primary text-text-primary text-xs"
                placeholder="name"
              />
              <input
                value={col.type}
                onChange={e => {
                  const c = [...newCols];
                  c[i] = { ...c[i], type: e.target.value };
                  setNewCols(c);
                }}
                className="flex-1 px-2 py-1.5 rounded border border-border-primary bg-bg-primary text-text-primary text-xs"
                placeholder="type"
              />
              {i > 0 && (
                <button onClick={() => setNewCols(newCols.filter((_, j) => j !== i))} className="p-1 text-red-400">
                  <Trash2 className="w-3.5 h-3.5" />
                </button>
              )}
            </div>
          ))}
          <button
            onClick={() => setNewCols([...newCols, { name: '', type: 'text', primaryKey: false, nullable: true, unique: false }])}
            className="text-xs text-purple-400 hover:text-purple-300"
          >
            + Add column
          </button>
        </div>
      </div>
    </SidePanel>
  );
}
