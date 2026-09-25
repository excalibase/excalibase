import { useState } from 'react';
import { Loader2 } from 'lucide-react';
import { InlineConfirm } from './InlineConfirm';
import { apiErrorMessage, type CollectionRef, type IndexSpec, type MongoDocument } from '../../api/documents';
import { useCreateIndex, useDocumentIndexes, useDropIndex } from '../../hooks/useDocuments';
import { toEditable } from '../../utils/extendedJson';

type Direction = '1' | '-1' | 'text' | '2dsphere' | 'hashed';

function keyValue(direction: Direction): 1 | -1 | string {
  if (direction === '1') return 1;
  if (direction === '-1') return -1;
  return direction;
}

function describeKeys(index: MongoDocument): string {
  const keys = toEditable(index.key ?? {}) as Record<string, unknown>;
  return Object.entries(keys)
    .map(([field, dir]) => `${field}: ${JSON.stringify(dir)}`)
    .join(', ');
}

export function IndexesPanel({ collectionRef }: { readonly collectionRef: CollectionRef }) {
  const indexes = useDocumentIndexes(collectionRef, true);
  const create = useCreateIndex(collectionRef);
  const drop = useDropIndex(collectionRef);
  const [field, setField] = useState('');
  const [direction, setDirection] = useState<Direction>('1');
  const [unique, setUnique] = useState(false);
  const [name, setName] = useState('');
  const [error, setError] = useState<string | null>(null);

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!field.trim()) {
      setError('Name the field to index');
      return;
    }
    const spec: IndexSpec = { keys: { [field.trim()]: keyValue(direction) }, unique };
    if (name.trim()) spec.name = name.trim();
    setError(null);
    create.mutate(spec, {
      onSuccess: () => {
        setField('');
        setName('');
        setUnique(false);
      },
      onError: (err) => setError(apiErrorMessage(err)),
    });
  };

  const inputClass = 'px-2 py-1.5 rounded-md text-sm bg-surface-card border border-border-primary text-text-primary';

  return (
    <div className="space-y-4" data-testid="indexes-panel">
      <form onSubmit={submit} className="flex flex-wrap items-end gap-2">
        <label className="flex flex-col text-xs text-text-secondary gap-1">
          <span>Field</span>
          <input aria-label="Index field" value={field} onChange={(e) => setField(e.target.value)} className={inputClass} />
        </label>
        <label className="flex flex-col text-xs text-text-secondary gap-1">
          <span>Type</span>
          <select aria-label="Index type" value={direction} onChange={(e) => setDirection(e.target.value as Direction)} className={inputClass}>
            <option value="1">Ascending (1)</option>
            <option value="-1">Descending (-1)</option>
            <option value="text">text</option>
            <option value="2dsphere">2dsphere</option>
            <option value="hashed">hashed</option>
          </select>
        </label>
        <label className="flex flex-col text-xs text-text-secondary gap-1">
          <span>Name (optional)</span>
          <input aria-label="Index name" value={name} onChange={(e) => setName(e.target.value)} className={inputClass} />
        </label>
        <label className="flex items-center gap-1.5 text-sm text-text-secondary pb-1.5">
          <input type="checkbox" aria-label="Unique" checked={unique} onChange={(e) => setUnique(e.target.checked)} />
          <span>Unique</span>
        </label>
        <button
          type="submit"
          disabled={create.isPending}
          className="px-3 py-1.5 rounded-md text-sm bg-purple-600 text-white hover:bg-purple-500 disabled:opacity-50"
        >
          Create index
        </button>
      </form>
      {error && (
        <p role="alert" className="text-sm text-red-400">
          {error}
        </p>
      )}
      {indexes.isLoading && <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" />}
      {indexes.isError && (
        <p role="alert" className="text-sm text-red-400">
          {apiErrorMessage(indexes.error)}
        </p>
      )}
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs text-text-tertiary">
            <th className="py-2">Name</th>
            <th>Keys</th>
            <th>Unique</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {(indexes.data ?? []).map((index) => {
            const indexName = String(index.name);
            return (
              <tr key={indexName} className="border-t border-border-primary" data-testid="index-row">
                <td className="py-2 font-mono text-text-primary">{indexName}</td>
                <td className="font-mono text-text-secondary">{describeKeys(index)}</td>
                <td className="text-text-secondary">{index.unique ? 'yes' : ''}</td>
                <td className="text-right">
                  {indexName !== '_id_' && (
                    <InlineConfirm
                      label={`Drop index ${indexName}`}
                      question="Drop this index?"
                      busy={drop.isPending}
                      onConfirm={() => drop.mutate(indexName, { onError: (err) => setError(apiErrorMessage(err)) })}
                    />
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
