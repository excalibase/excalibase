import { Trash2 } from 'lucide-react';
import type { ColumnInfo } from '../../types/schema';

interface ColumnSchemaViewProps {
  readonly columns: ColumnInfo[];
  readonly onDropColumn: (columnName: string) => void;
}

export function ColumnSchemaView({ columns, onDropColumn }: ColumnSchemaViewProps) {
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="bg-surface-card">
          <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Name</th>
          <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Type</th>
          <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Nullable</th>
          <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Default</th>
          <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">PK</th>
          <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Unique</th>
          <th className="px-4 py-2 text-right text-xs font-medium text-text-secondary">Actions</th>
        </tr>
      </thead>
      <tbody>
        {columns.map(col => (
          <tr key={col.name} className="border-t border-border-primary hover:bg-surface-hover" data-testid={`column-row-${col.name}`}>
            <td className="px-4 py-2 text-text-primary font-medium">{col.name}</td>
            <td className="px-4 py-2 text-text-secondary font-mono text-xs">
              {col.dataType}{col.characterMaximumLength ? `(${col.characterMaximumLength})` : ''}
            </td>
            <td className="px-4 py-2">
              {col.nullable
                ? <span className="text-green-400 text-xs">YES</span>
                : <span className="text-red-400 text-xs">NO</span>}
            </td>
            <td className="px-4 py-2 text-text-tertiary text-xs font-mono">{col.defaultValue || '-'}</td>
            <td className="px-4 py-2">{col.primaryKey && <span className="text-yellow-400 text-xs font-medium">PK</span>}</td>
            <td className="px-4 py-2">{col.unique && <span className="text-blue-400 text-xs font-medium">UQ</span>}</td>
            <td className="px-4 py-2 text-right">
              <button onClick={() => onDropColumn(col.name)} className="p-1 text-text-tertiary hover:text-red-400">
                <Trash2 className="w-3.5 h-3.5" />
              </button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
