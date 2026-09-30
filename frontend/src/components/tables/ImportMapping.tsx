import {
  IDENTIFIER_PATTERN,
  IMPORT_COLUMN_TYPES,
  type ColumnChoice,
  type ImportColumnType,
  type ImportPreview,
  type TargetChoice,
} from '../../api/tableImport';

const inputClass =
  'w-full px-2 py-1.5 rounded border border-border-primary bg-bg-primary text-text-primary text-xs';
const SHOWN_ROWS = 10;

interface ImportMappingProps {
  readonly preview: ImportPreview;
  readonly target: TargetChoice;
  readonly existingTables: string[];
  readonly onTargetChange: (target: TargetChoice) => void;
  readonly onReadChange: (change: {
    hasHeader?: boolean;
    delimiter?: string;
    sheet?: string;
  }) => void;
}

// ImportMapping is step two: where the rows go and what each column becomes.
export function ImportMapping({
  preview,
  target,
  existingTables,
  onTargetChange,
  onReadChange,
}: ImportMappingProps) {
  const setColumn = (i: number, change: Partial<ColumnChoice>) => {
    const columns = target.columns.map((c, j) => (j === i ? { ...c, ...change } : c));
    onTargetChange({ ...target, columns });
  };
  const included = target.columns.filter((c) => c.include);

  return (
    <div className="space-y-4" data-testid="import-mapping">
      <ReadSettings preview={preview} onReadChange={onReadChange} />

      <div className="grid grid-cols-2 gap-2">
        <label className="text-xs text-text-secondary">
          <span>Mode</span>
          <select
            value={target.mode}
            onChange={(e) =>
              onTargetChange({ ...target, mode: e.target.value as TargetChoice['mode'] })
            }
            className={inputClass}
            data-testid="import-mode"
          >
            <option value="create">Create a new table</option>
            <option value="append">Append to a table</option>
          </select>
        </label>
        <label className="text-xs text-text-secondary">
          <span>Schema</span>
          <input
            value={target.schema}
            onChange={(e) => onTargetChange({ ...target, schema: e.target.value })}
            className={inputClass}
            data-testid="import-schema"
          />
        </label>
      </div>

      <label className="block text-xs text-text-secondary">
        <span>Table</span>
        {target.mode === 'append' ? (
          <select
            value={target.table}
            onChange={(e) => onTargetChange({ ...target, table: e.target.value })}
            className={inputClass}
            data-testid="import-table-name"
          >
            <option value="">Choose a table</option>
            {existingTables.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        ) : (
          <input
            value={target.table}
            onChange={(e) => onTargetChange({ ...target, table: e.target.value })}
            className={inputClass}
            data-testid="import-table-name"
          />
        )}
        {target.table && !IDENTIFIER_PATTERN.test(target.table) && (
          <span className="text-red-400">
            Use lower-case letters, digits and underscores, starting with a letter.
          </span>
        )}
      </label>

      <div>
        <span className="block text-xs font-medium text-text-secondary mb-1">Columns</span>
        {preview.columns.map((col, i) => {
          const choice = target.columns[i];
          return (
            <div key={col.source} className="flex items-center gap-2 mb-1.5">
              <input
                type="checkbox"
                checked={choice.include}
                onChange={(e) => setColumn(i, { include: e.target.checked })}
                aria-label={`Import ${col.sourceName || col.name}`}
                data-testid={`import-col-include-${i}`}
              />
              <input
                value={choice.name}
                onChange={(e) => setColumn(i, { name: e.target.value })}
                className={`${inputClass} ${IDENTIFIER_PATTERN.test(choice.name) ? '' : 'border-red-500'}`}
                title={col.sourceName}
                data-testid={`import-col-name-${i}`}
              />
              <select
                value={choice.type}
                onChange={(e) => setColumn(i, { type: e.target.value as ImportColumnType })}
                className={inputClass}
                data-testid={`import-col-type-${i}`}
              >
                {IMPORT_COLUMN_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </select>
            </div>
          );
        })}
      </div>

      {target.mode === 'create' && (
        <label className="block text-xs text-text-secondary">
          <span>Primary key</span>
          <select
            value={target.primaryKey}
            onChange={(e) => onTargetChange({ ...target, primaryKey: e.target.value })}
            className={inputClass}
            data-testid="import-primary-key"
          >
            <option value="">Add a generated id column</option>
            {included.map((c) => (
              <option key={c.name} value={c.name}>
                {c.name}
              </option>
            ))}
          </select>
        </label>
      )}

      <PreviewRows preview={preview} />
    </div>
  );
}

function ReadSettings({
  preview,
  onReadChange,
}: Pick<ImportMappingProps, 'preview' | 'onReadChange'>) {
  return (
    <div className="flex flex-wrap items-center gap-3 text-xs text-text-secondary">
      <span className="uppercase font-mono text-text-tertiary">{preview.format}</span>
      <label className="flex items-center gap-1">
        <input
          type="checkbox"
          checked={preview.hasHeader}
          onChange={(e) => onReadChange({ hasHeader: e.target.checked })}
          data-testid="import-has-header"
        />
        <span>First row is a header</span>
      </label>
      {preview.format === 'csv' && (
        <label className="flex items-center gap-1">
          <span>Separator</span>
          <select
            value={preview.delimiter ?? ','}
            onChange={(e) => onReadChange({ delimiter: e.target.value })}
            className="px-1 py-0.5 rounded border border-border-primary bg-bg-primary"
            data-testid="import-delimiter"
          >
            <option value=",">comma</option>
            <option value="tab">tab</option>
            <option value=";">semicolon</option>
            <option value="|">pipe</option>
          </select>
        </label>
      )}
      {preview.sheets && preview.sheets.length > 1 && (
        <label className="flex items-center gap-1">
          <span>Sheet</span>
          <select
            value={preview.sheet}
            onChange={(e) => onReadChange({ sheet: e.target.value })}
            className="px-1 py-0.5 rounded border border-border-primary bg-bg-primary"
            data-testid="import-sheet"
          >
            {preview.sheets.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>
      )}
    </div>
  );
}

// Preview rows have no identity of their own; their line in the file is one.
function shownRows(preview: ImportPreview): Array<{ id: string; cells: string[] }> {
  const offset = preview.hasHeader ? 2 : 1;
  return preview.rows.slice(0, SHOWN_ROWS).map((cells, n) => ({ id: `line-${n + offset}`, cells }));
}

// Cells are rendered as text by React; a formula is shown, never run.
function PreviewRows({ preview }: { readonly preview: ImportPreview }) {
  return (
    <div>
      <span className="block text-xs font-medium text-text-secondary mb-1">
        First rows ({Math.min(SHOWN_ROWS, preview.rows.length)} of {preview.sampledRows} read)
      </span>
      <div className="overflow-x-auto border border-border-primary rounded">
        <table className="text-xs text-text-primary">
          <thead>
            <tr>
              {preview.columns.map((c) => (
                <th
                  key={c.source}
                  className="px-2 py-1 text-left font-medium text-text-tertiary whitespace-nowrap"
                >
                  {c.sourceName || c.name}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {shownRows(preview).map((row) => (
              <tr key={row.id} className="border-t border-border-primary">
                {preview.columns.map((c) => (
                  <td key={c.source} className="px-2 py-1 whitespace-nowrap max-w-[12rem] truncate">
                    {row.cells[c.source] ?? ''}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
