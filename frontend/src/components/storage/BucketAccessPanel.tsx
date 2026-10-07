import { useState } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { Button } from '../Button';
import { useUpdateBucketAccess, type BucketAccess, type AccessScope } from '../../hooks/useStorage';
import { serverErrorMessage } from '../../utils/serverError';

// What a project's app users may do in a bucket, by the role in their token
// (EXC-560). "own" is the folder named by the signed-in user's id.
interface RuleRow {
  readonly role: string;
  readonly read: AccessScope;
  readonly write: AccessScope;
  readonly delete: AccessScope;
}

const OPERATIONS = ['read', 'write', 'delete'] as const;

// Mirrors the server's rule (storagesvc.ValidateBucketAccess).
export function roleNameProblem(name: string): string | null {
  if (name === '') return 'Name the role.';
  if (!/^[a-z_][a-z0-9_]{0,62}$/.test(name)) {
    return 'Use lowercase letters, digits and underscores, starting with a letter or underscore.';
  }
  return null;
}

function toRows(access: BucketAccess): RuleRow[] {
  return Object.entries(access).map(([role, rule]) => ({
    role,
    read: rule.read ?? '',
    write: rule.write ?? '',
    delete: rule.delete ?? '',
  }));
}

function toAccess(rows: RuleRow[]): BucketAccess {
  const access: BucketAccess = {};
  for (const row of rows) {
    const rule: BucketAccess[string] = {};
    for (const op of OPERATIONS) {
      if (row[op]) rule[op] = row[op];
    }
    access[row.role] = rule;
  }
  return access;
}

function rowsProblem(rows: RuleRow[]): string | null {
  const seen = new Set<string>();
  for (const row of rows) {
    const problem = roleNameProblem(row.role);
    if (problem) return `${row.role || 'A role'}: ${problem}`;
    if (seen.has(row.role)) return `${row.role} is listed twice.`;
    seen.add(row.role);
  }
  return null;
}

interface BucketAccessPanelProps {
  readonly projectId: string;
  readonly bucket: string;
  readonly access: BucketAccess;
}

export function BucketAccessPanel({ projectId, bucket, access }: BucketAccessPanelProps) {
  const [rows, setRows] = useState<RuleRow[]>(() => toRows(access));
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const update = useUpdateBucketAccess(projectId, bucket);

  const edit = (index: number, change: Partial<RuleRow>) => {
    setSaved(false);
    setRows((current) => current.map((row, i) => (i === index ? { ...row, ...change } : row)));
  };

  const save = async () => {
    const problem = rowsProblem(rows);
    if (problem) {
      setError(problem);
      return;
    }
    setError(null);
    try {
      await update.mutateAsync(toAccess(rows));
      setSaved(true);
    } catch (err) {
      setError(serverErrorMessage(err, 'The access rules were not saved'));
    }
  };

  return (
    <section aria-label="App access" className="px-6 py-4 border-b border-border-primary space-y-3">
      <div>
        <h3 className="text-sm font-semibold">App access</h3>
        <p className="text-xs text-text-tertiary">
          What your app&apos;s signed-in users may do here, by the role in their token. &quot;Own&quot; is the folder
          named by the user&apos;s id, e.g. <code>42/avatar.png</code>.
        </p>
      </div>
      {rows.length === 0 && (
        <p className="text-xs text-text-secondary">App users cannot reach this bucket. Add a role to let them.</p>
      )}
      {rows.map((row, index) => (
        <div key={index} className="flex items-center gap-2">
          <input
            type="text"
            value={row.role}
            aria-label="Role name"
            placeholder="authenticated"
            onChange={(e) => edit(index, { role: e.target.value })}
            className="w-40 px-2 py-1 bg-bg-secondary border border-border-primary rounded text-sm font-mono"
          />
          {OPERATIONS.map((op) => (
            <label key={op} className="flex items-center gap-1 text-xs text-text-secondary">
              {op}
              <select
                aria-label={`${row.role} ${op}`}
                value={row[op]}
                onChange={(e) => edit(index, { [op]: e.target.value as AccessScope })}
                className="px-1 py-1 bg-bg-secondary border border-border-primary rounded text-xs"
              >
                <option value="">none</option>
                <option value="own">own</option>
                <option value="all">all</option>
              </select>
            </label>
          ))}
          <Button
            size="sm"
            variant="ghost"
            aria-label={`Remove ${row.role}`}
            onClick={() => {
              setSaved(false);
              setRows((current) => current.filter((_, i) => i !== index));
            }}
          >
            <Trash2 className="w-3.5 h-3.5" />
          </Button>
        </div>
      ))}
      {error && <p role="alert" className="text-xs text-red-400">{error}</p>}
      <div className="flex items-center gap-2">
        <Button
          size="sm"
          variant="ghost"
          onClick={() => {
            setSaved(false);
            setRows((current) => [...current, { role: '', read: '', write: '', delete: '' }]);
          }}
        >
          <Plus className="w-3.5 h-3.5 mr-1" />
          Add role
        </Button>
        <Button size="sm" onClick={save} disabled={update.isPending}>
          {update.isPending ? 'Saving…' : 'Save access'}
        </Button>
        {saved && <span className="text-xs text-text-tertiary">Saved</span>}
      </div>
    </section>
  );
}
