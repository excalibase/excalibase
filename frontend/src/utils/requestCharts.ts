// Request charts (EXC-656): per-project traffic of the GraphQL, REST, auth and
// realtime surfaces, sampled by provisioning once a minute.
export type RequestRange = '1h' | '24h' | '7d';
export type RequestFamily = 'graphql' | 'rest' | 'auth';
export type ChartFamily = RequestFamily | 'realtime';

export const REQUEST_RANGES: RequestRange[] = ['1h', '24h', '7d'];
export const REQUEST_FAMILIES: RequestFamily[] = ['graphql', 'rest', 'auth'];
export const CHART_FAMILIES: ChartFamily[] = [...REQUEST_FAMILIES, 'realtime'];

export const FAMILY_LABELS: Record<ChartFamily, string> = {
  graphql: 'GraphQL',
  rest: 'REST',
  auth: 'Auth',
  realtime: 'Realtime',
};

export interface RequestChartPoint {
  timestamp: string;
  family: ChartFamily;
  requests: number;
  clientErrors: number;
  serverErrors: number;
  p50Ms: number | null;
  p95Ms: number | null;
  connections: number;
  connectionsOpened: number;
  messages: number;
}

export interface RequestCharts {
  projectId: string;
  range: RequestRange;
  from: string;
  to: string;
  stepSeconds: number;
  points: RequestChartPoint[];
  totals: RequestChartPoint[];
}

export function requestChartsPath(projectId: string, range: RequestRange): string {
  return `/provision/${encodeURIComponent(projectId)}/requests?range=${range}`;
}

// The start of every step in the range, aligned the way the server truncates.
function steps(charts: RequestCharts): number[] {
  const stepMs = charts.stepSeconds * 1000;
  if (stepMs <= 0) return [];
  const first = Math.floor(Date.parse(charts.from) / stepMs) * stepMs;
  const last = Date.parse(charts.to);
  const out: number[] = [];
  for (let at = first; at < last; at += stepMs) out.push(at);
  return out;
}

function byStep(charts: RequestCharts): Map<number, Partial<Record<ChartFamily, RequestChartPoint>>> {
  const map = new Map<number, Partial<Record<ChartFamily, RequestChartPoint>>>();
  for (const point of charts.points) {
    const at = Date.parse(point.timestamp);
    map.set(at, { ...map.get(at), [point.family]: point });
  }
  return map;
}

function series<T>(charts: RequestCharts, row: (points: Partial<Record<ChartFamily, RequestChartPoint>>) => T) {
  const points = byStep(charts);
  return steps(charts).map((at) => ({ timestamp: new Date(at).toISOString(), ...row(points.get(at) ?? {}) }));
}

export function requestSeries(charts: RequestCharts) {
  return series(charts, (points) => ({
    graphql: points.graphql?.requests ?? 0,
    rest: points.rest?.requests ?? 0,
    auth: points.auth?.requests ?? 0,
  }));
}

function percentOf(part: number, whole: number): number | null {
  return whole > 0 ? (part / whole) * 100 : null;
}

// Percent of the step's requests, over every request family.
export function errorRateSeries(charts: RequestCharts) {
  return series(charts, (points) => {
    const families = REQUEST_FAMILIES.map((family) => points[family]).filter((p): p is RequestChartPoint => !!p);
    const requests = families.reduce((sum, p) => sum + p.requests, 0);
    return {
      clientErrors: percentOf(families.reduce((sum, p) => sum + p.clientErrors, 0), requests),
      serverErrors: percentOf(families.reduce((sum, p) => sum + p.serverErrors, 0), requests),
    };
  });
}

export function latencySeries(charts: RequestCharts, family: RequestFamily) {
  return series(charts, (points) => ({
    p50: points[family]?.p50Ms ?? null,
    p95: points[family]?.p95Ms ?? null,
  }));
}

export function realtimeSeries(charts: RequestCharts) {
  return series(charts, (points) => ({
    connections: points.realtime?.connections ?? 0,
    messages: points.realtime?.messages ?? 0,
  }));
}

export interface FamilySummary {
  family: ChartFamily;
  requests: number;
  errorRate: number | null;
  p50Ms: number | null;
  p95Ms: number | null;
  connections: number;
  messages: number;
}

export function familySummaries(charts: RequestCharts): FamilySummary[] {
  return CHART_FAMILIES.map((family) => {
    const total = charts.totals.find((point) => point.family === family);
    return {
      family,
      requests: total?.requests ?? 0,
      errorRate: total ? percentOf(total.clientErrors + total.serverErrors, total.requests) : null,
      p50Ms: total?.p50Ms ?? null,
      p95Ms: total?.p95Ms ?? null,
      connections: total?.connections ?? 0,
      messages: total?.messages ?? 0,
    };
  });
}

export function formatLatency(ms: number | null): string {
  if (ms == null) return '—';
  if (ms < 1) return `${ms.toFixed(2)} ms`;
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

export function formatRate(percent: number | null): string {
  return percent == null ? '—' : `${percent.toFixed(1)}%`;
}

export function stepLabel(timestamp: string, range: RequestRange): string {
  const at = new Date(timestamp);
  return range === '7d'
    ? at.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit' })
    : at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}
