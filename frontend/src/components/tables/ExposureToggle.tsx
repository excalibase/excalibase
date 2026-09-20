import { Globe, GlobeLock, Loader2 } from 'lucide-react';

interface ExposureToggleProps {
  table: string;
  schema: string;
  exposed: boolean;
  pending: boolean;
  onToggle: () => void;
}

/**
 * One table's "reachable through the API" switch, sitting in the table list so
 * the whole project's API surface reads at a glance (EXC-400).
 *
 * Deliberately not a permission matrix. Exposure answers one question — is this
 * table part of the API end users can call — and the filled globe answers it
 * from across the room. Which rows a caller then sees is RLS's job.
 *
 * Colour is not the only signal: the two states use different icons and carry
 * their meaning in the accessible name, so the list still reads for anyone who
 * does not separate the greens from the greys.
 */
export function ExposureToggle({ table, schema, exposed, pending, onToggle }: ExposureToggleProps) {
  const label = exposed
    ? `${table} is reachable through the API — click to remove it`
    : `${table} is not reachable through the API — click to expose it`;

  return (
    <button
      type="button"
      onClick={onToggle}
      disabled={pending}
      aria-pressed={exposed}
      aria-label={label}
      title={`${schema}.${table} — ${exposed ? 'reachable through the API' : 'hidden from the API'}`}
      data-testid={`exposure-toggle-${table}`}
      className={`flex-shrink-0 mr-2 p-1.5 rounded-lg transition-colors disabled:opacity-50 ${
        exposed
          ? 'text-emerald-400 hover:bg-emerald-500/10'
          : 'text-text-tertiary hover:bg-surface-hover hover:text-text-secondary'
      }`}
    >
      {pending ? (
        <Loader2 className="w-4 h-4 animate-spin" />
      ) : exposed ? (
        <Globe className="w-4 h-4" />
      ) : (
        <GlobeLock className="w-4 h-4" />
      )}
    </button>
  );
}
