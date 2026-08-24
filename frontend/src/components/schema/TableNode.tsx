import { memo, useState } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Table2, ChevronDown, ChevronUp } from 'lucide-react';
import { cn } from '../../utils/cn';
import { TableNodeField } from './TableNodeField';
import type { ColumnInfo } from '../../types/schema';

export interface TableNodeData {
  label: string;
  columns: ColumnInfo[];
  foreignKeyColumns: Set<string>;
  [key: string]: unknown;
}

const INITIAL_VISIBLE = 8;

export const TableNode = memo(function TableNode({ data, selected }: NodeProps) {
  const [expanded, setExpanded] = useState(false);
  const nodeData = data as unknown as TableNodeData;
  const columns = nodeData.columns || [];
  const fkColumns = nodeData.foreignKeyColumns || new Set<string>();
  const visibleColumns = expanded ? columns : columns.slice(0, INITIAL_VISIBLE);
  const hiddenCount = columns.length - INITIAL_VISIBLE;

  return (
    <div
      className={cn(
        'bg-surface-card border-2 rounded-lg shadow-sm min-w-[220px] max-w-[320px]',
        selected ? 'border-accent-primary' : 'border-border-primary'
      )}
    >
      {/* Color bar */}
      <div className="h-1 bg-accent-primary rounded-t-md" />

      {/* Header */}
      <div className="flex items-center gap-2 h-9 px-3 bg-bg-tertiary border-b border-border-primary">
        <Table2 size={14} className="text-accent-primary shrink-0" />
        <span className="font-bold text-sm text-text-primary truncate">{nodeData.label}</span>
        <span className="text-xs text-text-tertiary ml-auto">{columns.length}</span>
      </div>

      {/* Fields */}
      <div>
        {visibleColumns.map((col) => (
          <TableNodeField key={col.name} column={col} isForeignKey={fkColumns.has(col.name)} />
        ))}
      </div>

      {/* Expand/collapse */}
      {hiddenCount > 0 && (
        <button
          className="flex items-center justify-center gap-1 w-full h-7 text-xs text-text-tertiary hover:text-text-primary hover:bg-surface-hover transition-colors border-t border-border-primary"
          onClick={() => setExpanded(!expanded)}
        >
          {expanded ? (
            <>
              <ChevronUp size={12} /> Show less
            </>
          ) : (
            <>
              <ChevronDown size={12} /> +{hiddenCount} more
            </>
          )}
        </button>
      )}

      {/* Connection handles */}
      <Handle type="target" position={Position.Left} className="!bg-accent-primary !w-2 !h-2" />
      <Handle type="source" position={Position.Right} className="!bg-accent-primary !w-2 !h-2" />
    </div>
  );
});
