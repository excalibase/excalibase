import { useEffect, useMemo, useState } from 'react';
import { useLogSearch } from './useProvisioning';

export const TIME_RANGES = [
  { label: '1h', hours: 1, lines: 50 },
  { label: '6h', hours: 6, lines: 200 },
  { label: '24h', hours: 24, lines: 500 },
  { label: '7d', hours: 168, lines: 1000 },
];

export function keptFor(hours: number): string {
  if (hours % 24 === 0) return hours === 24 ? '1 day' : `${hours / 24} days`;
  return `${hours} hours`;
}

// The server searches stored logs for terms up to this long; longer ones only
// filter the lines already loaded.
const MAX_SERVER_SEARCH = 200;
const SEARCH_DEBOUNCE_MS = 400;

export function lineColor(line: string): string {
  if (line.includes('ERROR')) return 'text-red-400';
  if (line.includes('WARNING') || line.includes('WARN')) return 'text-yellow-400';
  return 'text-text-secondary';
}

// Search term, time range and the lines they select. Typing filters the loaded
// lines at once; the term reaches the server, which searches stored logs when
// it keeps them, once typing pauses.
export function useLogExplorer(projectId: string) {
  const [search, setSearch] = useState('');
  const [rangeLabel, setRangeLabel] = useState(TIME_RANGES[0].label);
  const [retentionHours, setRetentionHours] = useState<number | undefined>();
  const [serverSearch, setServerSearch] = useState('');
  // The plan's retention (EXC-659) hides every range that reaches past it.
  const ranges = TIME_RANGES.filter((r, i) => i === 0 || !retentionHours || r.hours <= retentionHours);
  const range = ranges.find((r) => r.label === rangeLabel) ?? ranges.at(-1) ?? TIME_RANGES[0];

  useEffect(() => {
    const term = search.trim();
    const timer = setTimeout(() => setServerSearch(term.length <= MAX_SERVER_SEARCH ? term : ''), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [search]);

  const { data, isLoading, error } = useLogSearch(projectId, range.lines, { since: range.label, search: serverSearch });
  const rawLogs = data?.logs ?? '';
  useEffect(() => setRetentionHours(data?.retentionHours), [data?.retentionHours]);

  const logLines = useMemo(() => {
    const allLines = rawLogs.split('\n').filter(Boolean);
    if (!search.trim()) return allLines;
    const term = search.toLowerCase();
    return allLines.filter((l) => l.toLowerCase().includes(term));
  }, [rawLogs, search]);

  return { search, setSearch, ranges, range, setRangeLabel, retentionHours, logLines, isLoading, error, notOffered: data?.notOffered };
}
