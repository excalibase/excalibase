import { useState } from 'react';
import { Loader2, Plus } from 'lucide-react';
import { InlineConfirm } from './InlineConfirm';
import { apiErrorMessage } from '../../api/documents';
import { useCreateCollection, useDocumentCollections, useDropCollection } from '../../hooks/useDocuments';
import { cn } from '../../utils/cn';

interface CollectionSidebarProps {
  readonly projectId: string;
  readonly databases: readonly string[];
  readonly database: string;
  readonly collection: string;
  readonly onDatabase: (database: string) => void;
  readonly onCollection: (collection: string) => void;
}

const inputClass = 'w-full px-2 py-1.5 rounded-md text-sm bg-surface-card border border-border-primary text-text-primary';

export function CollectionSidebar({ projectId, databases, database, collection, onDatabase, onCollection }: CollectionSidebarProps) {
  const collections = useDocumentCollections(projectId, database);
  const create = useCreateCollection(projectId, database);
  const drop = useDropCollection(projectId, database);
  const [newDatabase, setNewDatabase] = useState('');
  const [newCollection, setNewCollection] = useState('');
  const [error, setError] = useState<string | null>(null);

  const createCollection = (event: React.FormEvent) => {
    event.preventDefault();
    const name = newCollection.trim();
    if (!name || !database) return;
    setError(null);
    create.mutate(name, {
      onSuccess: () => {
        setNewCollection('');
        onCollection(name);
      },
      onError: (err) => setError(apiErrorMessage(err)),
    });
  };

  const chooseNewDatabase = (event: React.FormEvent) => {
    event.preventDefault();
    if (newDatabase.trim()) {
      onDatabase(newDatabase.trim());
      setNewDatabase('');
    }
  };

  const dropCollection = (name: string) => {
    setError(null);
    drop.mutate(name, {
      onSuccess: () => {
        if (name === collection) onCollection('');
      },
      onError: (err) => setError(apiErrorMessage(err)),
    });
  };

  const knownDatabases = databases.includes(database) || !database ? databases : [...databases, database];

  return (
    <aside className="w-64 flex-shrink-0 space-y-4" data-testid="collection-sidebar">
      <label className="flex flex-col gap-1 text-xs font-medium text-text-secondary">
        <span>Database</span>
        <select aria-label="Database" value={database} onChange={(e) => onDatabase(e.target.value)} className={inputClass}>
          {knownDatabases.length === 0 && <option value="">No databases yet</option>}
          {knownDatabases.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
        </select>
      </label>
      <form onSubmit={chooseNewDatabase} className="flex gap-1">
        <input
          aria-label="New database name"
          placeholder="New database"
          value={newDatabase}
          onChange={(e) => setNewDatabase(e.target.value)}
          className={inputClass}
        />
        <button type="submit" aria-label="Use new database" className="px-2 rounded-md border border-border-primary hover:bg-surface-hover">
          <Plus className="w-3.5 h-3.5" />
        </button>
      </form>

      <div>
        <h4 className="text-xs font-semibold uppercase tracking-wider text-text-tertiary mb-2">Collections</h4>
        {collections.isLoading && <Loader2 className="w-4 h-4 animate-spin text-text-tertiary" />}
        {collections.isError && (
          <p role="alert" className="text-sm text-red-400">
            {apiErrorMessage(collections.error)}
          </p>
        )}
        <ul className="space-y-0.5">
          {(collections.data ?? []).map((item) => (
            <li key={item.name} className="flex items-center justify-between gap-1">
              <button
                type="button"
                onClick={() => onCollection(item.name)}
                className={cn(
                  'flex-1 text-left truncate px-2 py-1.5 rounded-md text-sm',
                  item.name === collection ? 'bg-purple-500/10 text-purple-400' : 'text-text-secondary hover:bg-surface-hover',
                )}
              >
                {item.name}
              </button>
              <InlineConfirm
                label={`Drop collection ${item.name}`}
                question="Drop?"
                busy={drop.isPending}
                onConfirm={() => dropCollection(item.name)}
              />
            </li>
          ))}
        </ul>
        {database && (
          <form onSubmit={createCollection} className="flex gap-1 mt-2">
            <input
              aria-label="New collection name"
              placeholder="New collection"
              value={newCollection}
              onChange={(e) => setNewCollection(e.target.value)}
              className={inputClass}
            />
            <button type="submit" aria-label="Create collection" className="px-2 rounded-md border border-border-primary hover:bg-surface-hover">
              <Plus className="w-3.5 h-3.5" />
            </button>
          </form>
        )}
        {error && (
          <p role="alert" className="text-sm text-red-400 mt-2">
            {error}
          </p>
        )}
      </div>
    </aside>
  );
}
