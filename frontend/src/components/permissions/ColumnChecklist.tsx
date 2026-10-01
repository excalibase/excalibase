import type { ColumnList } from '../../api/permissions';

interface ColumnChecklistProps {
  readonly legend: string;
  readonly columns: string[];
  readonly value: ColumnList;
  readonly error?: string;
  readonly onChange: (value: ColumnList) => void;
}

/** "All columns" ("*", including ones added later) or a list of the table's real columns. */
export function ColumnChecklist({ legend, columns, value, error, onChange }: ColumnChecklistProps) {
  const all = value === '*';
  const chosen = new Set(all ? columns : value);

  const toggle = (column: string) => {
    const next = chosen.has(column) ? [...chosen].filter((c) => c !== column) : [...chosen, column];
    // Keep the table's column order, whatever order they were ticked in.
    onChange(columns.filter((c) => next.includes(c)));
  };

  return (
    <fieldset className="space-y-2">
      <legend className="text-sm font-medium text-text-secondary">{legend}</legend>
      <div className="flex items-center gap-2">
        <label className="flex items-center gap-2 text-sm text-text-primary">
          <input type="checkbox" checked={all} onChange={() => onChange(all ? [] : '*')} className="rounded" />
          All columns
        </label>
        <span className="text-xs text-text-tertiary">(including columns added later)</span>
      </div>
      <div className="grid grid-cols-2 gap-1 pl-5">
        {columns.map((column) => (
          <label key={column} className="flex items-center gap-2 text-xs text-text-secondary font-mono">
            <input
              type="checkbox"
              checked={chosen.has(column)}
              disabled={all}
              onChange={() => toggle(column)}
              className="rounded"
            />
            {column}
          </label>
        ))}
      </div>
      {error && (
        <p className="text-xs text-red-400" data-testid="columns-error">
          {error}
        </p>
      )}
    </fieldset>
  );
}
