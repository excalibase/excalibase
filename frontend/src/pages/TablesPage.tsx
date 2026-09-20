import { useState, useMemo, useCallback } from 'react';
import { useParams } from 'react-router-dom';
import { Plus, Trash2, Table2, Columns3, Download, RefreshCw } from 'lucide-react';
import {
  useTables, useColumns, useDropTable, useAddColumn, useDropColumn,
  useRows, useInsertRow, useUpdateRow, useDeleteRow,
} from '../hooks/useSchema';
import { SidePanel } from '../components/ui/SidePanel';
import { ConfirmModal } from '../components/ui/ConfirmModal';
import { SkeletonTable } from '../components/ui/Skeleton';
import { DataGrid } from '../components/tables/DataGrid';
import { CreateTablePanel } from '../components/tables/CreateTablePanel';
import { ColumnSchemaView } from '../components/tables/ColumnSchemaView';
import { ExposureToggle } from '../components/tables/ExposureToggle';
import { useTableGrants, useSetTableExposed, isTableExposed } from '../hooks/useTableGrants';

// csvSafe stringifies an arbitrary cell value without falling through to
// "[object Object]" (S6551). Used for CSV export only.
function csvSafe(v: unknown): string {
  if (v === null || v === undefined) return '';
  if (typeof v === 'string') return v;
  if (typeof v === 'number' || typeof v === 'boolean' || typeof v === 'bigint') return String(v);
  try {
    return JSON.stringify(v);
  } catch {
    return '';
  }
}

export function TablesPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const pid = projectId ?? '';
  const { data: tables = [], isLoading } = useTables(pid);
  const [selectedTable, setSelectedTable] = useState<string>('');
  const { data: columns = [] } = useColumns(pid, selectedTable);
  const dropTable = useDropTable(pid);
  const addColumn = useAddColumn(pid);
  const dropColumn = useDropColumn(pid);
  const insertRow = useInsertRow(pid);
  const updateRow = useUpdateRow(pid);
  const deleteRow = useDeleteRow(pid);
  // Which tables the project's end users can reach. Developers are not
  // filtered: this page reads the tenant database through the control plane's
  // own /schema routes, which never consult a grant.
  const { data: grantSet } = useTableGrants(pid);
  const setExposed = useSetTableExposed(pid);

  // Pagination & sorting
  const [page, setPage] = useState(0);
  const [pageSize] = useState(50);
  const [sortCol, setSortCol] = useState<string>('');
  const [sortOrder, setSortOrder] = useState<'asc' | 'desc'>('asc');

  const { data: rowsData, isLoading: rowsLoading, isFetching: rowsFetching, refetch: refetchRows } = useRows(
    pid,
    selectedTable,
    {
      limit: pageSize, offset: page * pageSize,
      sort: sortCol || undefined, order: sortCol ? sortOrder : undefined,
    },
  );

  // View mode
  const [viewMode, setViewMode] = useState<'columns' | 'data'>('data');

  // Panel / modal state
  const [showCreateTable, setShowCreateTable] = useState(false);
  const [showAddColumn, setShowAddColumn] = useState(false);
  const [showInsertRow, setShowInsertRow] = useState(false);
  const [dropTarget, setDropTarget] = useState<{ type: 'table' | 'column' | 'row'; name: string; pkCol?: string } | null>(null);

  // Add column form
  const [newColName, setNewColName] = useState('');
  const [newColType, setNewColType] = useState('text');
  const [newColNullable, setNewColNullable] = useState(true);

  // Insert row form
  const [newRowData, setNewRowData] = useState<Record<string, string>>({});

  const pkColumn = useMemo(() => columns.find(c => c.primaryKey)?.name ?? '', [columns]);

  const handleSortChange = useCallback((col: string, order: 'asc' | 'desc') => {
    setSortCol(col);
    setSortOrder(order);
    setPage(0);
  }, []);

  const handleCellEdit = useCallback((tableName: string, pk: string, pkValue: string, columnName: string, value: string | null) => {
    updateRow.mutate({
      tableName,
      pk: { column: pk, value: pkValue },
      data: { [columnName]: value },
    });
  }, [updateRow]);

  const handleDeleteRow = useCallback((pk: string, pkValue: string) => {
    setDropTarget({ type: 'row', name: pkValue, pkCol: pk });
  }, []);

  const handleAddColumn = () => {
    if (!newColName.trim() || !selectedTable) return;
    addColumn.mutate(
      { tableName: selectedTable, name: newColName, type: newColType, nullable: newColNullable },
      { onSuccess: () => { setShowAddColumn(false); setNewColName(''); } },
    );
  };

  const handleInsertRow = () => {
    if (!selectedTable) return;
    insertRow.mutate(
      { tableName: selectedTable, data: newRowData },
      { onSuccess: () => { setShowInsertRow(false); setNewRowData({}); } },
    );
  };

  const handleDrop = () => {
    if (!dropTarget) return;
    if (dropTarget.type === 'table') {
      dropTable.mutate({ tableName: dropTarget.name, cascade: true }, { onSuccess: () => { setDropTarget(null); setSelectedTable(''); } });
    } else if (dropTarget.type === 'column') {
      dropColumn.mutate({ tableName: selectedTable, columnName: dropTarget.name }, { onSuccess: () => setDropTarget(null) });
    } else if (dropTarget.type === 'row' && dropTarget.pkCol) {
      deleteRow.mutate({ tableName: selectedTable, pk: { column: dropTarget.pkCol, value: dropTarget.name } }, { onSuccess: () => setDropTarget(null) });
    }
  };

  const handleExportCSV = () => {
    if (!rowsData?.rows || !rowsData.columns) return;
    const header = rowsData.columns.map(c => c.name).join(',');
    const rows = rowsData.rows.map(r => r.map(v => v === null ? '' : `"${csvSafe(v).replaceAll('"', '""')}"`).join(','));
    const csv = [header, ...rows].join('\n');
    const blob = new Blob([csv], { type: 'text/csv' });
    const a = Object.assign(document.createElement('a'), { href: URL.createObjectURL(blob), download: `${selectedTable}.csv` });
    a.click();
  };

  if (isLoading) return <SkeletonTable rows={8} cols={5} />;

  function getDropModalTitle(): string {
    if (dropTarget?.type === 'table') return 'Drop Table';
    if (dropTarget?.type === 'column') return 'Drop Column';
    return 'Delete Row';
  }

  function getDropModalMessage(): string {
    if (dropTarget?.type === 'table') return `Permanently delete "${dropTarget?.name}" and all data?`;
    if (dropTarget?.type === 'column') return `Remove column "${dropTarget?.name}"?`;
    return `Delete row with ${dropTarget?.pkCol}=${dropTarget?.name}?`;
  }

  const dropModalTitle = getDropModalTitle();
  const dropModalMessage = getDropModalMessage();

  return (
    <div className="flex gap-4 h-[calc(100vh-220px)]" data-testid="tables-page">
      {/* Sidebar: table list */}
      <div className="w-64 flex-shrink-0 border border-border-primary rounded-lg bg-surface-card overflow-hidden flex flex-col">
        <div className="flex items-center justify-between px-4 py-3 border-b border-border-primary">
          <span className="text-sm font-medium text-text-primary">Tables ({tables.length})</span>
          <button onClick={() => setShowCreateTable(true)} className="p-1.5 rounded-lg text-purple-400 hover:bg-purple-500/10" data-testid="new-table-btn">
            <Plus className="w-4 h-4" />
          </button>
        </div>
        <div className="flex-1 overflow-y-auto">
          {tables.map(t => {
            const exposed = isTableExposed(grantSet, t.schema || 'public', t.name);
            return (
              <div
                key={t.name}
                className={`group w-full flex items-center transition-colors ${selectedTable === t.name ? 'bg-purple-500/10' : 'hover:bg-surface-hover'}`}
              >
                <button
                  onClick={() => { setSelectedTable(t.name); setPage(0); setSortCol(''); }}
                  aria-current={selectedTable === t.name}
                  className={`flex-1 min-w-0 flex items-center gap-2 pl-4 py-2.5 text-sm text-left ${selectedTable === t.name ? 'text-purple-400' : 'text-text-secondary'}`}
                  data-testid={`table-item-${t.name}`}
                >
                  <Table2 className="w-4 h-4 flex-shrink-0" />
                  <span className="truncate">{t.name}</span>
                </button>
                <ExposureToggle
                  table={t.name}
                  schema={t.schema || 'public'}
                  exposed={exposed}
                  pending={setExposed.isPending && setExposed.variables?.table === t.name}
                  onToggle={() => setExposed.mutate({
                    schema: t.schema || 'public',
                    table: t.name,
                    exposed: !exposed,
                  })}
                />
              </div>
            );
          })}
          {tables.length === 0 && <p className="px-4 py-8 text-sm text-text-tertiary text-center">No tables yet</p>}
        </div>
      </div>

      {/* Main content area */}
      <div className="flex-1 border border-border-primary rounded-lg bg-surface-card overflow-hidden flex flex-col">
        {selectedTable ? (
          <>
            {/* Header */}
            <div className="flex items-center justify-between px-4 py-3 border-b border-border-primary">
              <div className="flex items-center gap-2">
                <Columns3 className="w-4 h-4 text-purple-400" />
                <span className="text-sm font-medium text-text-primary">{selectedTable}</span>
                {rowsData && <span className="text-xs text-text-tertiary">({rowsData.totalCount} rows)</span>}
              </div>
              <div className="flex items-center gap-1">
                <button onClick={() => setViewMode('data')} className={`px-2 py-1 text-xs rounded ${viewMode === 'data' ? 'bg-purple-500/10 text-purple-400' : 'text-text-tertiary hover:text-text-primary'}`}>Data</button>
                <button onClick={() => setViewMode('columns')} className={`px-2 py-1 text-xs rounded ${viewMode === 'columns' ? 'bg-purple-500/10 text-purple-400' : 'text-text-tertiary hover:text-text-primary'}`}>Schema</button>
                <div className="w-px h-4 bg-border-primary mx-1" />
                {viewMode === 'data' && (
                  <>
                    <button onClick={() => refetchRows()} disabled={rowsFetching} className="flex items-center gap-1 px-2 py-1 text-xs text-text-tertiary hover:text-text-primary hover:bg-surface-hover rounded disabled:opacity-50" data-testid="refresh-rows-btn" title="Reload rows from the database">
                      <RefreshCw className={`w-3 h-3 ${rowsFetching ? 'animate-spin' : ''}`} /> Refresh
                    </button>
                    <button onClick={() => setShowInsertRow(true)} className="flex items-center gap-1 px-2 py-1 text-xs text-green-400 hover:bg-green-500/10 rounded" data-testid="insert-row-btn">
                      <Plus className="w-3 h-3" /> Row
                    </button>
                    <button onClick={handleExportCSV} className="flex items-center gap-1 px-2 py-1 text-xs text-text-tertiary hover:text-text-primary hover:bg-surface-hover rounded" data-testid="export-csv-btn">
                      <Download className="w-3 h-3" /> CSV
                    </button>
                  </>
                )}
                {viewMode === 'columns' && (
                  <button onClick={() => setShowAddColumn(true)} className="flex items-center gap-1 px-2 py-1 text-xs text-purple-400 hover:bg-purple-500/10 rounded" data-testid="add-column-btn">
                    <Plus className="w-3 h-3" /> Column
                  </button>
                )}
                <button onClick={() => setDropTarget({ type: 'table', name: selectedTable })} className="flex items-center gap-1 px-2 py-1 text-xs text-red-400 hover:bg-red-500/10 rounded" data-testid="drop-table-btn">
                  <Trash2 className="w-3 h-3" /> Drop
                </button>
              </div>
            </div>

            {/* Content */}
            {viewMode === 'data' ? (
              <DataGrid
                rowsData={rowsData}
                rowsLoading={rowsLoading}
                pkColumn={pkColumn}
                selectedTable={selectedTable}
                sortCol={sortCol}
                sortOrder={sortOrder}
                page={page}
                pageSize={pageSize}
                onSortChange={handleSortChange}
                onPageChange={setPage}
                onCellEdit={handleCellEdit}
                onDeleteRow={handleDeleteRow}
              />
            ) : (
              <div className="flex-1 overflow-auto">
                <ColumnSchemaView columns={columns} onDropColumn={(name) => setDropTarget({ type: 'column', name })} />
              </div>
            )}
          </>
        ) : (
          <div className="flex items-center justify-center h-full text-text-tertiary text-sm">Select a table from the sidebar</div>
        )}
      </div>

      {/* Create Table SidePanel */}
      <CreateTablePanel open={showCreateTable} onClose={() => setShowCreateTable(false)} projectId={pid} />

      {/* Add Column SidePanel */}
      <SidePanel open={showAddColumn} onClose={() => setShowAddColumn(false)} title={`Add Column to ${selectedTable}`}
        footer={<button onClick={handleAddColumn} disabled={!newColName.trim() || addColumn.isPending} className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg disabled:opacity-50" data-testid="add-column-submit">{addColumn.isPending ? 'Adding...' : 'Add Column'}</button>}>
        <div className="space-y-4">
          <div>
            <label htmlFor="col-name-input" className="block text-sm font-medium text-text-secondary mb-1">Name</label>
            <input id="col-name-input" type="text" value={newColName} onChange={e => setNewColName(e.target.value)} className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500" data-testid="column-name-input" autoFocus />
          </div>
          <div>
            <label htmlFor="col-type-select" className="block text-sm font-medium text-text-secondary mb-1">Type</label>
            <select id="col-type-select" value={newColType} onChange={e => setNewColType(e.target.value)} className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm">
              {['text', 'integer', 'bigint', 'serial', 'boolean', 'timestamp', 'timestamptz', 'date', 'numeric', 'uuid', 'jsonb', 'varchar(255)'].map(t => <option key={t} value={t}>{t}</option>)}
            </select>
          </div>
          <label className="flex items-center gap-2 text-sm text-text-secondary"><input type="checkbox" checked={newColNullable} onChange={e => setNewColNullable(e.target.checked)} className="rounded" /> Nullable</label>
        </div>
      </SidePanel>

      {/* Insert Row SidePanel */}
      <SidePanel open={showInsertRow} onClose={() => setShowInsertRow(false)} title={`Insert Row into ${selectedTable}`}
        footer={<button onClick={handleInsertRow} disabled={insertRow.isPending} className="w-full px-4 py-2 bg-green-500 hover:bg-green-600 text-white text-sm font-medium rounded-lg disabled:opacity-50" data-testid="insert-row-submit">{insertRow.isPending ? 'Inserting...' : 'Insert Row'}</button>}>
        <div className="space-y-3">
          {columns.filter(c => !c.defaultValue?.includes('nextval')).map(col => (
            <div key={col.name}>
              <label htmlFor={`insert-col-${col.name}`} className="block text-xs font-medium text-text-secondary mb-1">{col.name} <span className="text-text-tertiary">({col.dataType})</span></label>
              <input id={`insert-col-${col.name}`} type="text" value={newRowData[col.name] ?? ''} onChange={e => setNewRowData(prev => ({ ...prev, [col.name]: e.target.value }))}
                className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm font-mono focus:outline-none focus:ring-2 focus:ring-purple-500"
                placeholder={col.nullable ? 'NULL' : 'required'} />
            </div>
          ))}
        </div>
      </SidePanel>

      {/* Drop Confirm */}
      <ConfirmModal
        open={!!dropTarget} onClose={() => setDropTarget(null)} onConfirm={handleDrop}
        title={dropModalTitle}
        message={dropModalMessage}
        confirmText={dropTarget?.type === 'table' ? dropTarget?.name : undefined}
        confirmLabel={dropTarget?.type === 'row' ? 'Delete' : 'Drop'}
        destructive loading={dropTable.isPending || dropColumn.isPending || deleteRow.isPending}
      />
    </div>
  );
}
