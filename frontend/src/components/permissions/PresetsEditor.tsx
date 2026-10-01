import { Plus, X } from 'lucide-react';
import { newPresetRow, type PresetRow } from './permissionForm';

interface PresetsEditorProps {
  readonly columns: string[];
  readonly rows: PresetRow[];
  readonly error?: string;
  readonly onChange: (rows: PresetRow[]) => void;
}

/** Column presets: a literal, or a session variable such as X-Excalibase-User-Id. */
export function PresetsEditor({ columns, rows, error, onChange }: PresetsEditorProps) {
  const update = (index: number, patch: Partial<PresetRow>) =>
    onChange(rows.map((row, i) => (i === index ? { ...row, ...patch } : row)));

  return (
    <fieldset className="space-y-2">
      <legend className="text-sm font-medium text-text-secondary">Column presets</legend>
      <p className="text-xs text-text-tertiary">
        The server fills these columns; the client cannot set them, even when they are also listed above.
      </p>
      {rows.map((row, i) => (
        <div key={row.key} className="flex gap-2">
          <select
            aria-label={`Preset column ${i + 1}`}
            value={row.column}
            onChange={(e) => update(i, { column: e.target.value })}
            className="flex-1 px-2 py-1.5 rounded border border-border-primary bg-bg-primary text-text-primary text-xs"
          >
            <option value="">column…</option>
            {columns.map((column) => (
              <option key={column} value={column}>
                {column}
              </option>
            ))}
          </select>
          <input
            aria-label={`Preset value ${i + 1}`}
            value={row.value}
            onChange={(e) => update(i, { value: e.target.value })}
            placeholder="literal or X-Excalibase-User-Id"
            className="flex-1 px-2 py-1.5 rounded border border-border-primary bg-bg-primary text-text-primary text-xs font-mono"
          />
          <button
            type="button"
            aria-label={`Remove preset ${i + 1}`}
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
            className="p-1 text-text-tertiary hover:text-red-400"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={() => onChange([...rows, newPresetRow()])}
        className="flex items-center gap-1 text-xs text-purple-400 hover:text-purple-300"
      >
        <Plus className="w-3 h-3" /> Add preset
      </button>
      {error && (
        <p className="text-xs text-red-400" data-testid="presets-error">
          {error}
        </p>
      )}
    </fieldset>
  );
}
