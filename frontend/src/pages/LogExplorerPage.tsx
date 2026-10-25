import { useState, useMemo, useEffect } from 'react';
import { useParams } from 'react-router-dom';
import { Loader2, Search, FileText } from 'lucide-react';
import { useLogSearch } from '../hooks/useProvisioning';
import { serverErrorMessage } from '../utils/serverError';

const TIME_RANGES = [
  { label: '1h', hours: 1, lines: 50 },
  { label: '6h', hours: 6, lines: 200 },
  { label: '24h', hours: 24, lines: 500 },
  { label: '7d', hours: 168, lines: 1000 },
];

function keptFor(hours: number): string {
  if (hours % 24 === 0) return hours === 24 ? '1 day' : `${hours / 24} days`;
  return `${hours} hours`;
}

// The server searches stored logs for terms up to this long; longer ones only
// filter the lines already loaded.
const MAX_SERVER_SEARCH = 200;
const SEARCH_DEBOUNCE_MS = 400;

function getLineColor(line: string): string {
  if (line.includes('ERROR')) return 'text-red-400';
  if (line.includes('WARNING') || line.includes('WARN')) return 'text-yellow-400';
  return 'text-text-secondary';
}

export function LogExplorerPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [search, setSearch] = useState('');
  const [rangeLabel, setRangeLabel] = useState(TIME_RANGES[0].label);
  const [retentionHours, setRetentionHours] = useState<number | undefined>();
  // The plan's retention (EXC-659) hides every range that reaches past it.
  const ranges = TIME_RANGES.filter((r, i) => i === 0 || !retentionHours || r.hours <= retentionHours);
  const range = ranges.find((r) => r.label === rangeLabel) ?? ranges[ranges.length - 1];
  const { label: since, lines } = range;
  const [serverSearch, setServerSearch] = useState('');

  // Typing filters the loaded lines at once; the term reaches the server, which
  // searches stored logs when it keeps them, once typing pauses.
  useEffect(() => {
    const term = search.trim();
    const timer = setTimeout(() => setServerSearch(term.length <= MAX_SERVER_SEARCH ? term : ''), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [search]);

  const { data, isLoading, error } = useLogSearch(projectId || '', lines, { since, search: serverSearch });
  const rawLogs = data?.logs ?? '';
  useEffect(() => setRetentionHours(data?.retentionHours), [data?.retentionHours]);

  const logLines = useMemo(() => {
    const allLines = rawLogs.split('\n').filter(Boolean);
    if (!search.trim()) return allLines;
    const term = search.toLowerCase();
    return allLines.filter((l) => l.toLowerCase().includes(term));
  }, [rawLogs, search]);

  return (
    <div data-testid="log-explorer-page">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-lg font-semibold text-text-primary flex items-center gap-2">
          <FileText className="w-5 h-5 text-purple-400" /> Log Explorer
        </h3>
      </div>

      {/* Controls */}
      <div className="flex items-center gap-3 mb-4">
        <div className="relative flex-1 max-w-md">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-text-tertiary" />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search logs..."
            className="w-full pl-9 pr-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
            data-testid="log-search-input"
          />
        </div>
        <div className="flex rounded-lg border border-border-primary overflow-hidden">
          {ranges.map((r) => (
            <button
              key={r.label}
              onClick={() => setRangeLabel(r.label)}
              className={`px-3 py-2 text-xs font-medium transition-colors ${
                r.label === range.label
                  ? 'bg-purple-500 text-white'
                  : 'bg-bg-primary text-text-tertiary hover:text-text-primary'
              }`}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>

      {retentionHours ? (
        <p className="mb-3 text-xs text-text-tertiary" data-testid="log-retention">
          Logs are kept for {keptFor(retentionHours)} on this plan.
        </p>
      ) : null}

      {/* Log viewer */}
      <div className="rounded-lg border border-border-primary bg-bg-primary overflow-hidden">
        {isLoading && (
          <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>
        )}
        {!isLoading && error && (
          <p role="alert" className="p-12 text-center text-red-400 text-sm" data-testid="logs-error">
            {serverErrorMessage(error, 'The logs could not be loaded')}
          </p>
        )}
        {!isLoading && !error && logLines.length === 0 && (
          <div className="p-12 text-center text-text-tertiary text-sm">
            {search ? 'No matching log lines' : 'No logs available'}
          </div>
        )}
        {!isLoading && logLines.length > 0 && (
          <div className="max-h-[600px] overflow-y-auto font-mono text-xs p-4 space-y-0.5">
            {logLines.map((line, i) => (
              <div key={`log-${i}-${line.slice(0, 30)}`} className={`py-0.5 ${getLineColor(line)}`}>
                {line}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
