import { useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Trash2 } from 'lucide-react';
import { SidePanel } from '../ui/SidePanel';
import { useCreateTable } from '../../hooks/useSchema';
import { permissionsKey } from '../../hooks/usePermissions';
import { apiErrorMessage, putTablePermission } from '../../api/permissions';
import { DEFAULT_ROLES, fullAccess, tableKey } from '../../utils/permissionModel';
import { serverErrorMessage } from '../../utils/serverError';

interface ColumnDraft {
  // _key is a stable identity for React keys, since column names + positions
  // are mutable while editing. Generated client-side; never sent to the API.
  _key: string;
  name: string;
  type: string;
  primaryKey: boolean;
  nullable: boolean;
  unique: boolean;
}

let __nextColKey = 0;
function newColKey(): string {
  __nextColKey += 1;
  return `col-${__nextColKey}`;
}

const DEFAULT_COLUMNS: ColumnDraft[] = [
  { _key: 'col-default-id', name: 'id', type: 'serial', primaryKey: true, nullable: false, unique: false },
];

function submitLabel(grantError: string | null, busy: boolean): string {
  if (grantError) return 'Close';
  return busy ? 'Creating...' : 'Create Table';
}

type ReadRole = (typeof DEFAULT_ROLES)[number];

const READ_OPTIONS: ReadonlyArray<{ role: ReadRole; label: string }> = [
  { role: 'anon', label: 'Anyone can read (anon)' },
  { role: 'user', label: 'Signed-in users can read (user)' },
];

const NO_READ: Record<ReadRole, boolean> = { anon: false, user: false };

interface CreateTablePanelProps {
  readonly open: boolean;
  readonly onClose: () => void;
  readonly projectId: string;
  // Developer and up may write permissions; others are not offered public read.
  readonly canGrantRead?: boolean;
}

export function CreateTablePanel({ open, onClose, projectId, canGrantRead = false }: CreateTablePanelProps) {
  const createTable = useCreateTable(projectId);
  const qc = useQueryClient();
  const [newTableName, setNewTableName] = useState('');
  const [newCols, setNewCols] = useState<ColumnDraft[]>([...DEFAULT_COLUMNS]);
  // Hasura's default: a new table is reachable by nobody until a permission says so.
  const [readRoles, setReadRoles] = useState<Record<ReadRole, boolean>>(NO_READ);
  const [granting, setGranting] = useState(false);
  const [grantError, setGrantError] = useState<string | null>(null);
  const [createError, setCreateError] = useState<string | null>(null);
  // A fast double click lands twice before isPending re-renders the button.
  const inFlight = useRef(false);

  const reset = () => {
    setNewTableName('');
    setNewCols([...DEFAULT_COLUMNS]);
    setReadRoles(NO_READ);
    setGrantError(null);
    setCreateError(null);
  };

  const handleClose = () => {
    onClose();
    reset();
  };

  // The table stays when a grant fails: the error says so instead of undoing it.
  const grantRead = async (tableName: string): Promise<string[]> => {
    const failures: string[] = [];
    for (const { role } of READ_OPTIONS.filter((option) => canGrantRead && readRoles[option.role])) {
      try {
        await putTablePermission(projectId, tableKey('public', tableName), role, 'select', fullAccess('select'));
      } catch (err) {
        failures.push(`${role} read could not be granted (${apiErrorMessage(err, 'unknown error')})`);
      }
    }
    return failures;
  };

  const handleCreate = async () => {
    if (!newTableName.trim() || inFlight.current) return;
    inFlight.current = true;
    setCreateError(null);
    try {
      await createTable.mutateAsync({
        name: newTableName,
        // Strip the client-only _key before sending to the API.
        columns: newCols.map(({ _key: _, ...c }) => ({ ...c, default: undefined })),
      });
    } catch (err) {
      setCreateError(serverErrorMessage(err, 'The table was not created'));
      return;
    } finally {
      inFlight.current = false;
    }
    setGranting(true);
    const failures = await grantRead(newTableName);
    setGranting(false);
    qc.invalidateQueries({ queryKey: permissionsKey(projectId) });
    if (failures.length > 0) {
      setGrantError(
        `Table ${newTableName} was created, but ${failures.join('; ')}. Set it on the table's permissions page.`,
      );
      return;
    }
    handleClose();
  };

  const busy = createTable.isPending || granting;

  return (
    <SidePanel
      open={open}
      onClose={handleClose}
      title="Create Table"
      footer={
        <div className="space-y-2">
          {grantError && (
            <p role="alert" className="text-xs text-red-400">
              {grantError}
            </p>
          )}
          {createError && (
            <p role="alert" className="text-xs text-red-400" data-testid="create-table-error">
              {createError}
            </p>
          )}
          <button
            onClick={grantError ? handleClose : handleCreate}
            disabled={!grantError && (!newTableName.trim() || busy)}
            className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg disabled:opacity-50"
            data-testid="create-table-submit"
          >
            {submitLabel(grantError, busy)}
          </button>
        </div>
      }
    >
      <div className="space-y-4">
        <div>
          <label htmlFor="new-table-name" className="block text-sm font-medium text-text-secondary mb-1">Table Name</label>
          <input
            id="new-table-name"
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
          <span className="block text-sm font-medium text-text-secondary mb-1">Columns</span>
          {newCols.map((col, i) => (
            <div key={col._key} className="flex gap-2 mb-2">
              <input
                value={col.name}
                onChange={e => {
                  const c = [...newCols];
                  c[i] = { ...c[i], name: e.target.value };
                  setNewCols(c);
                }}
                className="flex-1 px-2 py-1.5 rounded border border-border-primary bg-bg-primary text-text-primary text-xs"
                placeholder="e.g. email"
              />
              <input
                value={col.type}
                onChange={e => {
                  const c = [...newCols];
                  c[i] = { ...c[i], type: e.target.value };
                  setNewCols(c);
                }}
                className="flex-1 px-2 py-1.5 rounded border border-border-primary bg-bg-primary text-text-primary text-xs"
                placeholder="e.g. text"
              />
              {i > 0 && (
                <button onClick={() => setNewCols(newCols.filter((_, j) => j !== i))} className="p-1 text-red-400" aria-label="Remove column">
                  <Trash2 className="w-3.5 h-3.5" />
                </button>
              )}
            </div>
          ))}
          <button
            onClick={() => setNewCols([...newCols, { _key: newColKey(), name: '', type: 'text', primaryKey: false, nullable: true, unique: false }])}
            className="text-xs text-purple-400 hover:text-purple-300"
          >
            + Add column
          </button>
        </div>
        {canGrantRead && (
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium text-text-secondary mb-1">API access</legend>
            {READ_OPTIONS.map(({ role, label }) => (
              <label key={role} className="flex items-center gap-2 text-sm text-text-primary">
                <input
                  type="checkbox"
                  checked={readRoles[role]}
                  onChange={(e) => setReadRoles((current) => ({ ...current, [role]: e.target.checked }))}
                  className="rounded"
                />
                {label}
              </label>
            ))}
            <p className="text-xs text-text-tertiary">
              Unchecked, nobody can reach it through the API (except secret service keys) until you add permissions.
              Checked, that role may read every row and column; set finer rules on the table's permissions page.
            </p>
          </fieldset>
        )}
      </div>
    </SidePanel>
  );
}
