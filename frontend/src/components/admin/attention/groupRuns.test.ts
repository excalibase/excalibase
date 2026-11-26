import { describe, expect, test } from 'vitest';
import { groupRuns } from './groupRuns';

const row = (id: string, reason: string) => ({ id, reason });

describe('groupRuns', () => {
  test('rows that share a reason one after another become one group, in their order', () => {
    const runs = groupRuns([row('a', 'proxy'), row('b', 'proxy'), row('c', 'proxy'), row('d', 'mining'), row('e', 'proxy')], (r) => r.reason);
    expect(runs.map((run) => [run.key, run.items.length])).toEqual([['proxy', 3], ['mining', 1], ['proxy', 1]]);
    expect(runs[0].items.map((r) => r.id)).toEqual(['a', 'b', 'c']);
  });

  test('nothing is reordered, so rows arriving later never move the ones already shown', () => {
    const first = groupRuns([row('a', 'x'), row('b', 'y')], (r) => r.reason);
    const more = groupRuns([row('a', 'x'), row('b', 'y'), row('c', 'y')], (r) => r.reason);
    expect(more[0]).toEqual(first[0]);
    expect(more[1].items.map((r) => r.id)).toEqual(['b', 'c']);
  });

  test('an empty list has no groups', () => {
    expect(groupRuns([], (r: { reason: string }) => r.reason)).toEqual([]);
  });
});
