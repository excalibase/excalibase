import { Loader2 } from 'lucide-react';
import type { MySQLCatalog } from '../api/mysqlCatalog';

interface MySQLVersionPickerProps {
  readonly catalog: MySQLCatalog | undefined;
  readonly isLoading: boolean;
  readonly error: unknown;
  readonly version: string;
  readonly onVersionChange: (version: string) => void;
}

function tileClass(selected: boolean): string {
  return selected
    ? 'border-accent-primary bg-accent-primary/10'
    : 'border-border-primary bg-bg-tertiary hover:border-border-secondary';
}

// One MySQL series is offered (8.4 LTS), so the newest version is a safe
// preselection: later minor upgrades stay inside the series.
export function MySQLVersionPicker({ catalog, isLoading, error, version, onVersionChange }: MySQLVersionPickerProps) {
  return (
    <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4" data-testid="mysql-version-section">
      <div>
        <h2 className="font-semibold text-text-primary">MySQL Version</h2>
        <p className="text-xs text-text-tertiary mt-1">MySQL 8.4 LTS. Minor upgrades move a project to a newer 8.4 release.</p>
      </div>
      {isLoading && (
        <div className="flex items-center gap-2 text-sm text-text-tertiary">
          <Loader2 className="w-4 h-4 animate-spin" /> Loading supported versions...
        </div>
      )}
      {!isLoading && error != null && (
        <p className="text-sm text-color-error" data-testid="mysql-version-error">
          Supported versions could not be loaded, so none can be offered. Retry in a moment.
        </p>
      )}
      {!isLoading && error == null && (
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
          {(catalog?.versions ?? []).map(({ version: option }) => (
            <button
              key={option}
              type="button"
              aria-pressed={version === option}
              data-testid={`mysql-version-${option}`}
              onClick={() => onVersionChange(option)}
              className={`p-4 rounded-xl border-2 text-left transition-all ${tileClass(version === option)}`}
            >
              <p className="font-semibold text-text-primary text-sm">MySQL {option}</p>
              {option === catalog?.defaultVersion && <p className="text-xs text-text-tertiary mt-0.5">Newest</p>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
