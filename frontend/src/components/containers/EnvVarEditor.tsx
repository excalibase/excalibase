import { Plus, Trash2 } from 'lucide-react';
import { DATABASE_VARIABLES, type VarKind } from '../../api/apps';
import { emptyEnvRow, needsSecretValue, type EnvRow } from './appFormModel';
import { cn } from '../../utils/cn';

export const inputClass =
  'w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-sm text-text-primary placeholder-text-tertiary focus:outline-none focus:ring-2 focus:ring-purple-500';

interface EnvVarEditorProps {
  readonly rows: EnvRow[];
  readonly errors?: Record<number, string>;
  readonly databaseName?: string;
  readonly onChange: (rows: EnvRow[]) => void;
}

interface RowProps {
  readonly index: number;
  readonly row: EnvRow;
  readonly databaseName?: string;
  readonly update: (patch: Partial<EnvRow>) => void;
}

const smallButton =
  'px-2.5 py-1.5 text-xs rounded-lg border border-border-primary text-text-secondary hover:bg-surface-hover transition-colors whitespace-nowrap';

// The value is never shown: a stored secret reads "Set" and can only be
// replaced by typing a new one.
function SecretField({ index, row, update }: Omit<RowProps, 'databaseName'>) {
  if (!needsSecretValue(row)) {
    return (
      <div className="flex items-center gap-2 flex-1 min-w-0">
        <span className="text-sm text-text-secondary" data-testid={`env-secret-set-${index}`}>
          ••••••• Set
        </span>
        <button
          type="button"
          onClick={() => update({ replacing: true, secretValue: '' })}
          className={smallButton}
          data-testid={`env-secret-replace-${index}`}
        >
          Replace
        </button>
      </div>
    );
  }
  return (
    <div className="flex items-center gap-2 flex-1 min-w-0">
      <input
        type="password"
        autoComplete="new-password"
        aria-label={`Secret value for variable ${index + 1}`}
        placeholder={row.storedSecret ? 'new secret value' : 'secret value'}
        value={row.secretValue}
        onChange={(e) => update({ secretValue: e.target.value })}
        className={inputClass}
        data-testid={`env-secret-value-${index}`}
      />
      {row.storedSecret && (
        <button
          type="button"
          onClick={() => update({ replacing: false, secretValue: '' })}
          className={smallButton}
          data-testid={`env-secret-keep-${index}`}
        >
          Keep current
        </button>
      )}
    </div>
  );
}

function ValueField({ index, row, databaseName, update }: RowProps) {
  if (row.kind === 'reference') {
    return (
      <div className="flex items-center gap-2 flex-1 min-w-0">
        <span className="text-xs text-text-tertiary whitespace-nowrap">
          database {databaseName ?? '(none)'} ·
        </span>
        <select
          aria-label={`Database value for variable ${index + 1}`}
          value={row.variable}
          onChange={(e) => update({ variable: e.target.value })}
          className={inputClass}
          data-testid={`env-variable-${index}`}
        >
          {DATABASE_VARIABLES.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
        </select>
      </div>
    );
  }
  if (row.kind === 'secret') return <SecretField index={index} row={row} update={update} />;
  return (
    <input
      aria-label={`Value for variable ${index + 1}`}
      placeholder="value"
      value={row.value}
      onChange={(e) => update({ value: e.target.value })}
      className={cn(inputClass, 'flex-1 min-w-0')}
      data-testid={`env-value-${index}`}
    />
  );
}

export function EnvVarEditor({ rows, errors, databaseName, onChange }: EnvVarEditorProps) {
  const updateRow = (index: number, patch: Partial<EnvRow>) =>
    onChange(rows.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  const removeRow = (index: number) => onChange(rows.filter((_, i) => i !== index));

  return (
    <div className="space-y-2" data-testid="env-editor">
      {rows.length === 0 && (
        <p className="text-sm text-text-tertiary">
          No variables yet. Your container starts with none of its own.
        </p>
      )}
      {rows.map((row, index) => (
        <div key={row.id} data-testid={`env-row-${index}`}>
          <div className="flex items-center gap-2">
            <input
              aria-label={`Name of variable ${index + 1}`}
              placeholder="NAME"
              value={row.name}
              onChange={(e) => updateRow(index, { name: e.target.value })}
              className={cn(inputClass, 'font-mono max-w-[12rem]')}
              data-testid={`env-name-${index}`}
            />
            <select
              aria-label={`Kind of variable ${index + 1}`}
              value={row.kind}
              onChange={(e) => updateRow(index, { kind: e.target.value as VarKind })}
              className={cn(inputClass, 'max-w-[11rem]')}
              data-testid={`env-kind-${index}`}
            >
              <option value="literal">Value</option>
              <option value="reference" disabled={!databaseName}>
                Database connection
              </option>
              <option value="secret">Secret</option>
            </select>
            <ValueField
              index={index}
              row={row}
              databaseName={databaseName}
              update={(patch) => updateRow(index, patch)}
            />
            <button
              type="button"
              onClick={() => removeRow(index)}
              aria-label={`Remove variable ${index + 1}`}
              className="p-2 rounded-lg text-text-tertiary hover:text-red-400 hover:bg-surface-hover"
              data-testid={`env-remove-${index}`}
            >
              <Trash2 className="w-4 h-4" />
            </button>
          </div>
          {errors?.[index] && <p className="mt-1 text-xs text-red-400">{errors[index]}</p>}
        </div>
      ))}
      <button
        type="button"
        onClick={() => onChange([...rows, emptyEnvRow()])}
        className="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-lg border border-border-primary text-text-secondary hover:bg-surface-hover transition-colors"
        data-testid="env-add"
      >
        <Plus className="w-4 h-4" /> Add variable
      </button>
    </div>
  );
}
