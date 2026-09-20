import { Loader2 } from 'lucide-react';
import { findMajor, type PostgresCatalog, type PostgresMajor } from '../api/postgresCatalog';

interface PostgresVersionPickerProps {
  readonly catalog: PostgresCatalog | undefined;
  readonly isLoading: boolean;
  readonly error: unknown;
  readonly version: string;
  readonly onVersionChange: (version: string) => void;
  readonly documentDb: boolean;
  readonly onDocumentDbChange: (documentDb: boolean) => void;
}

function versionTileClass(disabled: boolean, selected: boolean): string {
  if (disabled) return 'border-border-primary bg-bg-secondary opacity-50 cursor-not-allowed';
  if (selected) return 'border-accent-primary bg-accent-primary/10';
  return 'border-border-primary bg-bg-tertiary hover:border-border-secondary';
}

// documentDbBlockedReason says why the DocumentDB option is closed, or returns
// null when it is open. The wording for an incapable major comes from the
// catalogue over the wire, so this page cannot contradict the rule the API
// enforces.
function documentDbBlockedReason(version: string, entry: PostgresMajor | undefined): string | null {
  if (!version) return 'Choose a PostgreSQL version first — DocumentDB is not available on every major.';
  if (!entry) return null;
  if (entry.documentDb) return null;
  return entry.documentDbUnavailableReason ?? `DocumentDB is not available on PostgreSQL ${version}.`;
}

// PostgresVersionPicker collects the one choice the platform will not make for
// the customer. There is deliberately no pre-selected major: the version their
// data lives on for the life of the project is theirs to pick, and a default
// would be picked silently.
export function PostgresVersionPicker({
  catalog,
  isLoading,
  error,
  version,
  onVersionChange,
  documentDb,
  onDocumentDbChange,
}: PostgresVersionPickerProps) {
  const selected = findMajor(catalog, version);
  const blockedReason = documentDbBlockedReason(version, selected);

  return (
    <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4" data-testid="pg-version-section">
      <div>
        <h2 className="font-semibold text-text-primary">PostgreSQL Version</h2>
        <p className="text-xs text-text-tertiary mt-1">
          Choose the major your data will live on. It is fixed for the life of the project.
        </p>
      </div>

      {isLoading && (
        <div className="flex items-center gap-2 text-sm text-text-tertiary">
          <Loader2 className="w-4 h-4 animate-spin" /> Loading supported versions...
        </div>
      )}

      {!isLoading && error != null && (
        <p className="text-sm text-color-error" data-testid="pg-version-error">
          Supported versions could not be loaded, so none can be offered. Retry in a moment.
        </p>
      )}

      {!isLoading && error == null && (
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-3" data-testid="pg-version-selector">
          {(catalog?.majors ?? []).map((entry) => (
            <button
              key={entry.major}
              type="button"
              disabled={!entry.available}
              aria-pressed={version === entry.major}
              data-testid={`pg-version-${entry.major}`}
              onClick={() => onVersionChange(entry.major)}
              className={`p-4 rounded-xl border-2 text-left transition-all ${versionTileClass(!entry.available, version === entry.major)}`}
            >
              <p className="font-semibold text-text-primary text-sm">PostgreSQL {entry.major}</p>
              <p className="text-xs text-text-tertiary mt-0.5">
                {entry.available ? (entry.documentDb ? 'DocumentDB available' : 'PostgreSQL only') : 'Not published yet'}
              </p>
            </button>
          ))}
        </div>
      )}

      <div className="border-t border-border-primary pt-4" data-testid="documentdb-section">
        <label className="flex items-start gap-3 cursor-pointer">
          <input
            type="checkbox"
            checked={documentDb}
            disabled={blockedReason !== null}
            data-testid="documentdb-toggle"
            onChange={(e) => onDocumentDbChange(e.target.checked)}
            className="mt-0.5 w-4 h-4 accent-accent-primary disabled:cursor-not-allowed"
          />
          <span>
            <span className="block text-sm font-medium text-text-primary">DocumentDB (MongoDB-compatible API)</span>
            <span className="block text-xs text-text-tertiary mt-0.5">
              Adds the DocumentDB extension so Mongo clients can talk to this project. Available at creation only.
            </span>
          </span>
        </label>
        {blockedReason && (
          <p className="text-xs text-amber-400 mt-2 pl-7" data-testid="documentdb-reason">
            {blockedReason}
          </p>
        )}
      </div>
    </div>
  );
}
