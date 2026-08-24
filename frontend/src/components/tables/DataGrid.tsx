import { useState, useMemo, useCallback } from 'react';
import { Trash2, ChevronLeft, ChevronRight } from 'lucide-react';
import {
  useReactTable, getCoreRowModel, flexRender,
  type ColumnDef,
} from '@tanstack/react-table';
import type { ColumnMeta, RowsResult } from '../../types/schema';
import { SkeletonTable } from '../ui/Skeleton';

interface DataGridProps {
  readonly rowsData: RowsResult | undefined;
  readonly rowsLoading: boolean;
  readonly pkColumn: string;
  readonly selectedTable: string;
  readonly sortCol: string;
  readonly sortOrder: 'asc' | 'desc';
  readonly page: number;
  readonly pageSize: number;
  readonly onSortChange: (col: string, order: 'asc' | 'desc') => void;
  readonly onPageChange: (page: number) => void;
  readonly onCellEdit: (tableName: string, pkColumn: string, pkValue: string, columnName: string, value: string | null) => void;
  readonly onDeleteRow: (pkColumn: string, pkValue: string) => void;
}

interface EditingCell {
  readonly row: number;
  readonly col: number;
  readonly value: string;
}

// safeString avoids the [object Object] sonar trap (S6551) when stringifying
// arbitrary cell values pulled out of the row buffer.
function safeString(val: unknown): string {
  if (val === null || val === undefined) return '';
  if (typeof val === 'string') return val;
  if (typeof val === 'number' || typeof val === 'boolean' || typeof val === 'bigint') {
    return String(val);
  }
  try {
    return JSON.stringify(val);
  } catch {
    return '';
  }
}

interface BuildColumnDefArgs {
  readonly col: ColumnMeta;
  readonly i: number;
  readonly sortCol: string;
  readonly sortOrder: 'asc' | 'desc';
  readonly onSortChange: (col: string, order: 'asc' | 'desc') => void;
  readonly editingCell: EditingCell | null;
  readonly commitEdit: (rowIdx: number, colIdx: number, columnName: string, val: unknown, newVal: string) => void;
  readonly setEditingCell: (c: EditingCell | null) => void;
}

function buildColumnDef(a: BuildColumnDefArgs): ColumnDef<unknown[], unknown> {
  const { col, i, sortCol, sortOrder, onSortChange, editingCell, commitEdit, setEditingCell } = a;
  return {
    id: col.name,
    header: () => (
      <HeaderCell column={col} sortCol={sortCol} sortOrder={sortOrder} onSortChange={onSortChange} />
    ),
    accessorFn: (row: unknown[]) => row[i],
    cell: (info) => {
      const val = info.getValue();
      const tableRow = info.row;
      const isEditing = editingCell?.row === tableRow.index && editingCell?.col === i;
      if (isEditing) {
        return (
          <CellEditor
            val={val}
            onCommit={(newVal) => commitEdit(tableRow.index, i, col.name, val, newVal)}
            onCancel={() => setEditingCell(null)}
          />
        );
      }
      return (
        <CellView
          val={val}
          onActivate={() => setEditingCell({ row: tableRow.index, col: i, value: safeString(val) })}
        />
      );
    },
  };
}

interface HeaderCellProps {
  readonly column: ColumnMeta;
  readonly sortCol: string;
  readonly sortOrder: 'asc' | 'desc';
  readonly onSortChange: (col: string, order: 'asc' | 'desc') => void;
}

function HeaderCell({ column, sortCol, sortOrder, onSortChange }: HeaderCellProps) {
  const handleClick = () => {
    if (sortCol === column.name) {
      onSortChange(column.name, sortOrder === 'asc' ? 'desc' : 'asc');
    } else {
      onSortChange(column.name, 'asc');
    }
  };
  return (
    <button
      onClick={handleClick}
      className="flex items-center gap-1 text-left"
    >
      {column.name}
      <span className="text-text-tertiary text-[10px]">{column.dataType}</span>
      {sortCol === column.name && <span className="text-purple-400">{sortOrder === 'asc' ? '↑' : '↓'}</span>}
    </button>
  );
}

interface CellEditorProps {
  readonly val: unknown;
  readonly onCommit: (newVal: string) => void;
  readonly onCancel: () => void;
}

function CellEditor({ val, onCommit, onCancel }: CellEditorProps) {
  const initial = val === null ? '' : safeString(val);
  const handleBlur = (e: React.FocusEvent<HTMLInputElement>) => {
    onCommit(e.target.value);
  };
  const handleKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
    if (e.key === 'Escape') onCancel();
  };
  return (
    <input
      autoFocus
      defaultValue={initial}
      className="w-full px-1 py-0.5 bg-bg-primary border border-purple-500 rounded text-xs font-mono outline-none"
      onBlur={handleBlur}
      onKeyDown={handleKey}
    />
  );
}

interface CellViewProps {
  readonly val: unknown;
  readonly onActivate: () => void;
}

function CellView({ val, onActivate }: CellViewProps) {
  const handleKey = (e: React.KeyboardEvent<HTMLButtonElement>) => {
    if (e.key === 'Enter' || e.key === ' ') onActivate();
  };
  return (
    <button
      type="button"
      onDoubleClick={onActivate}
      onKeyDown={handleKey}
      className="cursor-text bg-transparent border-0 p-0 text-left w-full"
    >
      {val === null ? <span className="text-text-tertiary italic">NULL</span> : safeString(val).slice(0, 1000)}
    </button>
  );
}

export function DataGrid({
  rowsData, rowsLoading, pkColumn, selectedTable,
  sortCol, sortOrder, page, pageSize,
  onSortChange, onPageChange, onCellEdit, onDeleteRow,
}: DataGridProps) {
  const [editingCell, setEditingCell] = useState<EditingCell | null>(null);

  // commitEdit lifts the side-effectful "did the cell value change?" branch out
  // of the render path so the cell renderer stays shallow (S2004).
  const commitEdit = useCallback((tableRowIndex: number, _colIndex: number, columnName: string, val: unknown, newVal: string) => {
    if (newVal !== safeString(val) && pkColumn && rowsData?.rows) {
      const pkIdx = rowsData.columns.findIndex((c: ColumnMeta) => c.name === pkColumn);
      const pkValue = safeString(rowsData.rows[tableRowIndex][pkIdx]);
      onCellEdit(selectedTable, pkColumn, pkValue, columnName, newVal || null);
    }
    setEditingCell(null);
  }, [pkColumn, rowsData, selectedTable, onCellEdit]);

  const tableColumns = useMemo(() => {
    if (!rowsData?.columns) return [];
    return rowsData.columns.map((col: ColumnMeta, i: number) =>
      buildColumnDef({
        col, i, sortCol, sortOrder, onSortChange,
        editingCell, commitEdit, setEditingCell,
      })
    );
  }, [rowsData, sortCol, sortOrder, editingCell, onSortChange, commitEdit]);

  const table = useReactTable({
    data: rowsData?.rows ?? [],
    columns: tableColumns,
    getCoreRowModel: getCoreRowModel(),
    manualSorting: true,
    manualPagination: true,
  });

  if (rowsLoading) {
    return <div className="p-4"><SkeletonTable rows={5} cols={4} /></div>;
  }

  return (
    <>
      <div className="flex-1 overflow-auto">
        <table className="w-full text-xs">
          <thead className="sticky top-0 z-10">
            {table.getHeaderGroups().map(hg => (
              <tr key={hg.id} className="bg-surface-card">
                {hg.headers.map(h => (
                  <th key={h.id} className="px-3 py-2 text-left font-medium text-text-secondary border-b border-border-primary whitespace-nowrap">
                    {flexRender(h.column.columnDef.header, h.getContext())}
                  </th>
                ))}
                <th className="px-2 py-2 border-b border-border-primary w-8" />
              </tr>
            ))}
          </thead>
          <tbody>
            {table.getRowModel().rows.map(row => (
              <tr key={row.id} className="hover:bg-surface-hover border-b border-border-primary last:border-0">
                {row.getVisibleCells().map(cell => (
                  <td key={cell.id} className="px-3 py-1.5 text-text-primary font-mono whitespace-nowrap max-w-xs truncate">
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </td>
                ))}
                <td className="px-2 py-1.5">
                  {pkColumn && rowsData?.rows && (
                    <DeleteRowButton
                      rows={rowsData.rows}
                      columns={rowsData.columns}
                      pkColumn={pkColumn}
                      rowIndex={row.index}
                      onDeleteRow={onDeleteRow}
                    />
                  )}
                </td>
              </tr>
            ))}
            {(rowsData?.rows?.length ?? 0) === 0 && (
              <tr><td colSpan={99} className="px-4 py-8 text-center text-text-tertiary text-sm">No data</td></tr>
            )}
          </tbody>
        </table>
      </div>

      {rowsData && (
        <div className="flex items-center justify-between px-4 py-2 border-t border-border-primary text-xs text-text-tertiary">
          <span>Page {page + 1} of {Math.max(1, Math.ceil(rowsData.totalCount / pageSize))}</span>
          <div className="flex items-center gap-2">
            <button onClick={() => onPageChange(Math.max(0, page - 1))} disabled={page === 0} className="p-1 rounded hover:bg-surface-hover disabled:opacity-30">
              <ChevronLeft className="w-4 h-4" />
            </button>
            <button onClick={() => onPageChange(page + 1)} disabled={(page + 1) * pageSize >= rowsData.totalCount} className="p-1 rounded hover:bg-surface-hover disabled:opacity-30">
              <ChevronRight className="w-4 h-4" />
            </button>
          </div>
        </div>
      )}
    </>
  );
}

interface DeleteRowButtonProps {
  readonly rows: ReadonlyArray<ReadonlyArray<unknown>>;
  readonly columns: ReadonlyArray<ColumnMeta>;
  readonly pkColumn: string;
  readonly rowIndex: number;
  readonly onDeleteRow: (pkColumn: string, pkValue: string) => void;
}

function DeleteRowButton({ rows, columns, pkColumn, rowIndex, onDeleteRow }: DeleteRowButtonProps) {
  const handleClick = () => {
    const pkIdx = columns.findIndex((c) => c.name === pkColumn);
    const pkVal = safeString(rows[rowIndex][pkIdx]);
    onDeleteRow(pkColumn, pkVal);
  };
  return (
    <button
      onClick={handleClick}
      className="p-0.5 text-text-tertiary hover:text-red-400"
    >
      <Trash2 className="w-3 h-3" />
    </button>
  );
}
