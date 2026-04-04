import { useState, useMemo } from 'react';
import { Trash2, ChevronLeft, ChevronRight } from 'lucide-react';
import {
  useReactTable, getCoreRowModel, flexRender,
  type ColumnDef,
} from '@tanstack/react-table';
import type { ColumnMeta, RowsResult } from '../../types/schema';
import { SkeletonTable } from '../ui/Skeleton';

interface DataGridProps {
  rowsData: RowsResult | undefined;
  rowsLoading: boolean;
  pkColumn: string;
  selectedTable: string;
  sortCol: string;
  sortOrder: 'asc' | 'desc';
  page: number;
  pageSize: number;
  onSortChange: (col: string, order: 'asc' | 'desc') => void;
  onPageChange: (page: number) => void;
  onCellEdit: (tableName: string, pkColumn: string, pkValue: string, columnName: string, value: string | null) => void;
  onDeleteRow: (pkColumn: string, pkValue: string) => void;
}

export function DataGrid({
  rowsData, rowsLoading, pkColumn, selectedTable,
  sortCol, sortOrder, page, pageSize,
  onSortChange, onPageChange, onCellEdit, onDeleteRow,
}: DataGridProps) {
  const [editingCell, setEditingCell] = useState<{ row: number; col: number; value: string } | null>(null);

  const tableColumns = useMemo(() => {
    if (!rowsData?.columns) return [];
    return rowsData.columns.map((col: ColumnMeta, i: number) => ({
      id: col.name,
      header: () => (
        <button
          onClick={() => {
            if (sortCol === col.name) {
              onSortChange(col.name, sortOrder === 'asc' ? 'desc' : 'asc');
            } else {
              onSortChange(col.name, 'asc');
            }
          }}
          className="flex items-center gap-1 text-left"
        >
          {col.name}
          <span className="text-text-tertiary text-[10px]">{col.dataType}</span>
          {sortCol === col.name && <span className="text-purple-400">{sortOrder === 'asc' ? '\u2191' : '\u2193'}</span>}
        </button>
      ),
      accessorFn: (row: unknown[]) => row[i],
      cell: ({ getValue, row: tableRow }: { getValue: () => unknown; row: { index: number } }) => {
        const val = getValue();
        const isEditing = editingCell?.row === tableRow.index && editingCell?.col === i;
        if (isEditing) {
          return (
            <input
              autoFocus
              defaultValue={val === null ? '' : String(val)}
              className="w-full px-1 py-0.5 bg-bg-primary border border-purple-500 rounded text-xs font-mono outline-none"
              onBlur={(e) => {
                const newVal = e.target.value;
                if (newVal !== String(val ?? '') && pkColumn && rowsData?.rows) {
                  const pkIdx = rowsData.columns.findIndex((c: ColumnMeta) => c.name === pkColumn);
                  const pkValue = String(rowsData.rows[tableRow.index][pkIdx]);
                  onCellEdit(selectedTable, pkColumn, pkValue, col.name, newVal || null);
                }
                setEditingCell(null);
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
                if (e.key === 'Escape') setEditingCell(null);
              }}
            />
          );
        }
        return (
          <span
            onDoubleClick={() => setEditingCell({ row: tableRow.index, col: i, value: String(val ?? '') })}
            className="cursor-text"
          >
            {val === null ? <span className="text-text-tertiary italic">NULL</span> : String(val).slice(0, 1000)}
          </span>
        );
      },
    }));
  }, [rowsData, sortCol, sortOrder, editingCell, pkColumn, selectedTable, onCellEdit, onSortChange]);

  const table = useReactTable({
    data: rowsData?.rows ?? [],
    columns: tableColumns as ColumnDef<unknown[], unknown>[],
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
                    <button
                      onClick={() => {
                        const pkIdx = rowsData.columns.findIndex((c: ColumnMeta) => c.name === pkColumn);
                        const pkVal = String(rowsData.rows[row.index][pkIdx]);
                        onDeleteRow(pkColumn, pkVal);
                      }}
                      className="p-0.5 text-text-tertiary hover:text-red-400"
                    >
                      <Trash2 className="w-3 h-3" />
                    </button>
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

      {/* Pagination */}
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
