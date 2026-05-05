import { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import { Loader2, Search, FileText } from 'lucide-react';
import { useLogs } from '../hooks/useProvisioning';

const TIME_RANGES = [
  { label: '1h', lines: 50 },
  { label: '6h', lines: 200 },
  { label: '24h', lines: 500 },
  { label: '7d', lines: 1000 },
];

function getLineColor(line: string): string {
  if (line.includes('ERROR')) return 'text-red-400';
  if (line.includes('WARNING') || line.includes('WARN')) return 'text-yellow-400';
  return 'text-text-secondary';
}

export function LogExplorerPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [search, setSearch] = useState('');
  const [rangeIdx, setRangeIdx] = useState(0);
  const lines = TIME_RANGES[rangeIdx].lines;

  const { data: rawLogs = '', isLoading } = useLogs(projectId || '', lines);

  const logLines = useMemo(() => {
    const allLines = typeof rawLogs === 'string' ? rawLogs.split('\n').filter(Boolean) : [];
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
          {TIME_RANGES.map((r, i) => (
            <button
              key={r.label}
              onClick={() => setRangeIdx(i)}
              className={`px-3 py-2 text-xs font-medium transition-colors ${
                i === rangeIdx
                  ? 'bg-purple-500 text-white'
                  : 'bg-bg-primary text-text-tertiary hover:text-text-primary'
              }`}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>

      {/* Log viewer */}
      <div className="rounded-lg border border-border-primary bg-bg-primary overflow-hidden">
        {isLoading && (
          <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>
        )}
        {!isLoading && logLines.length === 0 && (
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
