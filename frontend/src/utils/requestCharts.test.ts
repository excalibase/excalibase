import { describe, test, expect } from 'vitest';
import {
  requestChartsPath,
  requestSeries,
  errorRateSeries,
  latencySeries,
  realtimeSeries,
  familySummaries,
  type RequestCharts,
} from './requestCharts';

const from = '2026-10-25T09:00:00Z';
const to = '2026-10-25T09:05:00Z';

function charts(overrides: Partial<RequestCharts> = {}): RequestCharts {
  return {
    projectId: 'proj-1',
    range: '1h',
    from,
    to,
    stepSeconds: 60,
    points: [
      { timestamp: '2026-10-25T09:01:00Z', family: 'graphql', requests: 90, clientErrors: 5, serverErrors: 4, p50Ms: 4, p95Ms: 300, connections: 0, connectionsOpened: 0, messages: 0 },
      { timestamp: '2026-10-25T09:01:00Z', family: 'auth', requests: 10, clientErrors: 1, serverErrors: 0, p50Ms: 20, p95Ms: 40, connections: 0, connectionsOpened: 0, messages: 0 },
      { timestamp: '2026-10-25T09:03:00Z', family: 'realtime', requests: 0, clientErrors: 0, serverErrors: 0, p50Ms: null, p95Ms: null, connections: 3, connectionsOpened: 1, messages: 12 },
    ],
    totals: [
      { timestamp: from, family: 'graphql', requests: 90, clientErrors: 5, serverErrors: 4, p50Ms: 4, p95Ms: 300, connections: 0, connectionsOpened: 0, messages: 0 },
      { timestamp: from, family: 'realtime', requests: 0, clientErrors: 0, serverErrors: 0, p50Ms: null, p95Ms: null, connections: 3, connectionsOpened: 1, messages: 12 },
    ],
    ...overrides,
  };
}

describe('request charts', () => {
  test('asks for the range by name', () => {
    expect(requestChartsPath('proj 1', '24h')).toBe('/provision/proj%201/requests?range=24h');
  });

  test('every step of the range is a point, quiet steps count zero', () => {
    const rows = requestSeries(charts());
    expect(rows.map((row) => row.timestamp)).toEqual([
      '2026-10-25T09:00:00.000Z', '2026-10-25T09:01:00.000Z', '2026-10-25T09:02:00.000Z',
      '2026-10-25T09:03:00.000Z', '2026-10-25T09:04:00.000Z',
    ]);
    expect(rows[1]).toMatchObject({ graphql: 90, rest: 0, auth: 10 });
    expect(rows[2]).toMatchObject({ graphql: 0, rest: 0, auth: 0 });
  });

  test('error rate is over every request family, and a step without requests has none', () => {
    const rows = errorRateSeries(charts());
    expect(rows[1].clientErrors).toBeCloseTo(6);
    expect(rows[1].serverErrors).toBeCloseTo(4);
    expect(rows[2].clientErrors).toBeNull();
    expect(rows[2].serverErrors).toBeNull();
  });

  test('latency is one family at a time, a gap where it had no requests', () => {
    const rows = latencySeries(charts(), 'graphql');
    expect(rows[1]).toMatchObject({ p50: 4, p95: 300 });
    expect(rows[2]).toMatchObject({ p50: null, p95: null });
  });

  test('realtime shows open connections and delivered messages', () => {
    const rows = realtimeSeries(charts());
    expect(rows[3]).toMatchObject({ connections: 3, messages: 12 });
    expect(rows[0]).toMatchObject({ connections: 0, messages: 0 });
  });

  test('summaries list each family with its error rate, empty families included', () => {
    const summaries = familySummaries(charts());
    expect(summaries.map((s) => s.family)).toEqual(['graphql', 'rest', 'auth', 'realtime']);
    expect(summaries[0]).toMatchObject({ requests: 90, errorRate: 10, p50Ms: 4, p95Ms: 300 });
    expect(summaries[1]).toMatchObject({ requests: 0, errorRate: null, p95Ms: null });
    expect(summaries[3]).toMatchObject({ connections: 3, messages: 12 });
  });

  test('a range with no data still draws its empty steps', () => {
    expect(requestSeries(charts({ points: [], totals: [] }))).toHaveLength(5);
  });
});
