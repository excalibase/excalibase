import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, test, expect } from 'vitest';
import type { Alert } from './alerts';
import type { PerformanceSummary, QueryStat, WaitEvent } from '../hooks/usePerformance';

// Each fixture is what the server sends: a Go test marshals the server's own
// type and must equal the same file. Each key table below is checked against
// the Studio type by the compiler, so a field the Studio reads but the server
// never sends fails here instead of showing a blank (EXC-893).
function sentKeys(fixture: string): string[] {
  const raw = readFileSync(resolve(process.cwd(), '../shared-fixtures/wire', fixture), 'utf8');
  return Object.keys(JSON.parse(raw)).sort();
}

function readKeys(table: Record<string, true>): string[] {
  return Object.keys(table).sort();
}

const alertKeys: Record<keyof Alert, true> = {
  id: true, projectId: true, severity: true, message: true, metric: true,
  value: true, threshold: true, timestamp: true, resolved: true,
};

const performanceSummaryKeys: Record<keyof PerformanceSummary, true> = {
  activeConnections: true, totalConnections: true, cacheHitRatio: true, databaseSize: true,
  slowQueryCount: true, avgQueryTimeMs: true, available: true, unavailableReason: true,
};

const queryStatKeys: Record<keyof QueryStat, true> = {
  query: true, calls: true, totalExecTimeMs: true, avgExecTimeMs: true,
  minExecTimeMs: true, maxExecTimeMs: true, rows: true,
};

const waitEventKeys: Record<keyof WaitEvent, true> = { waitEventType: true, waitEvent: true, count: true };

describe('what Studio reads is what the server sends', () => {
  test.each([
    ['alert.json', alertKeys],
    ['performance-summary.json', performanceSummaryKeys],
    ['query-stat.json', queryStatKeys],
    ['wait-event.json', waitEventKeys],
  ])('%s', (fixture, table) => {
    expect(readKeys(table)).toEqual(sentKeys(fixture));
  });
});
