import { KeyRound, Link as LinkIcon } from 'lucide-react';
import { cn } from '../../utils/cn';
import type { ColumnInfo } from '../../types/schema';

interface TableNodeFieldProps {
  readonly column: ColumnInfo;
  readonly isForeignKey: boolean;
}

export function TableNodeField({ column, isForeignKey }: TableNodeFieldProps): JSX.Element {
  const typeDisplay = column.characterMaximumLength
    ? `${column.dataType}(${column.characterMaximumLength})`
    : column.dataType;

  return (
    <div className="flex items-center h-8 px-3 border-t border-border-primary hover:bg-surface-hover transition-colors text-sm">
      <div className="flex items-center gap-1 w-5 shrink-0">
        {column.primaryKey && <KeyRound size={12} className="text-amber-500" />}
        {isForeignKey && <LinkIcon size={12} className="text-blue-400" />}
      </div>

      <span
        className={cn(
          'flex-1 min-w-0 truncate',
          (column.primaryKey || column.unique) && 'font-semibold',
          isForeignKey && 'text-blue-400'
        )}
      >
        {column.name}
      </span>

      <span className="text-xs text-text-tertiary truncate ml-2 max-w-[120px]">
        {typeDisplay}
      </span>

      {!column.nullable && (
        <span className="text-xs text-color-error ml-1">*</span>
      )}
    </div>
  );
}
