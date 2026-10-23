// Slow queries (EXC-654): the same shape for Postgres and MySQL.
export type SlowQuerySort = 'total' | 'mean';
export type SlowQueryEngine = 'postgresql' | 'mysql';

export interface SlowQuery {
  query: string;
  calls: number;
  totalTimeMs: number;
  meanTimeMs: number;
  rows: number;
}

export interface SlowQueryReport {
  engine: SlowQueryEngine;
  enabled: boolean;
  sort: SlowQuerySort;
  queries: SlowQuery[];
  canReset: boolean;
  canEnable: boolean;
}

export function slowQueriesPath(projectId: string, sort: SlowQuerySort, limit: number): string {
  return `/provision/${encodeURIComponent(projectId)}/slow-queries/?sort=${sort}&limit=${limit}`;
}

export function formatDuration(ms: number): string {
  if (ms < 0.01) return '<0.01 ms';
  if (ms < 1) return `${ms.toFixed(2)} ms`;
  if (ms < 1000) return `${ms.toFixed(1)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)} s`;
  return `${(ms / 60_000).toFixed(1)} min`;
}

// Percent of the listed statements' total time this one took.
export function shareOfTime(query: Pick<SlowQuery, 'totalTimeMs'>, queries: Pick<SlowQuery, 'totalTimeMs'>[]): number {
  const total = queries.reduce((sum, q) => sum + q.totalTimeMs, 0);
  return total > 0 ? (query.totalTimeMs / total) * 100 : 0;
}

export function enableNotice(engine: SlowQueryEngine): string {
  return engine === 'mysql'
    ? 'The platform is granted read access to this database’s statement digests; the database keeps running.'
    : 'The database restarts once to load pg_stat_statements, which takes about a minute; connections drop meanwhile.';
}
