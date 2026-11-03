import { describe, test, expect } from 'vitest';
import { enableNotice, formatDuration, shareOfTime, slowQueriesPath } from './slowQueries';

describe('slow queries', () => {
  test('asks the server for one sort and a bounded number of statements', () => {
    expect(slowQueriesPath('proj-1', 'mean', 50)).toBe('/provision/proj-1/slow-queries/?sort=mean&limit=50');
    expect(slowQueriesPath('proj 1', 'total', 20)).toBe('/provision/proj%201/slow-queries/?sort=total&limit=20');
  });

  test('durations read in the unit that fits them', () => {
    expect(formatDuration(0.004)).toBe('<0.01 ms');
    expect(formatDuration(0.42)).toBe('0.42 ms');
    expect(formatDuration(12.345)).toBe('12.3 ms');
    expect(formatDuration(1250)).toBe('1.25 s');
    expect(formatDuration(125_000)).toBe('2.1 min');
  });

  test("a statement's share is of all the time listed", () => {
    const queries = [{ totalTimeMs: 75 }, { totalTimeMs: 25 }];
    expect(shareOfTime(queries[0], queries)).toBe(75);
    expect(shareOfTime({ totalTimeMs: 0 }, [{ totalTimeMs: 0 }])).toBe(0);
  });

  test('turning statistics on says a Postgres database restarts and MySQL does not', () => {
    expect(enableNotice('postgresql')).toMatch(/restarts once/);
    expect(enableNotice('mysql')).not.toMatch(/restart/);
  });
});
