import { useState } from 'react';
import { ownerOnlyExpression } from '../../utils/permissionRules';

interface BoolExpEditorProps {
  readonly id: 'filter' | 'check';
  readonly label: string;
  readonly help: string;
  readonly value: string;
  readonly error?: string;
  readonly columns: string[];
  readonly onChange: (text: string) => void;
}

const inputClass =
  'w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-xs font-mono focus:outline-none focus:ring-2 focus:ring-purple-500';
const chipClass =
  'px-2 py-1 text-xs rounded-md border border-border-primary text-text-secondary hover:text-text-primary hover:bg-surface-hover disabled:opacity-50';

/** A Hasura boolean expression as JSON, with the two presets most tables need. */
export function BoolExpEditor({ id, label, help, value, error, columns, onChange }: BoolExpEditorProps) {
  const [ownerColumn, setOwnerColumn] = useState('');
  const inputId = `perm-${id}`;

  return (
    <div className="space-y-2">
      <label htmlFor={inputId} className="block text-sm font-medium text-text-secondary">
        {label}
      </label>
      <p className="text-xs text-text-tertiary">{help}</p>
      <div className="flex flex-wrap items-center gap-2">
        <button type="button" className={chipClass} onClick={() => onChange('{}')}>
          Without any checks
        </button>
        <select
          aria-label={`Owner column for ${label}`}
          value={ownerColumn}
          onChange={(e) => setOwnerColumn(e.target.value)}
          className="px-2 py-1 text-xs rounded-md border border-border-primary bg-bg-primary text-text-primary"
        >
          <option value="">owner column…</option>
          {columns.map((column) => (
            <option key={column} value={column}>
              {column}
            </option>
          ))}
        </select>
        <button
          type="button"
          className={chipClass}
          disabled={!ownerColumn}
          onClick={() => onChange(JSON.stringify(ownerOnlyExpression(ownerColumn), null, 2))}
        >
          Owner only
        </button>
      </div>
      <textarea
        id={inputId}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        rows={6}
        spellCheck={false}
        className={inputClass}
        placeholder={'{ "owner_id": { "_eq": "X-Excalibase-User-Id" } }'}
        aria-invalid={!!error}
        aria-describedby={error ? `${inputId}-error` : undefined}
      />
      {error && (
        <p id={`${inputId}-error`} className="text-xs text-red-400" data-testid={`${id}-error`}>
          {error}
        </p>
      )}
    </div>
  );
}
