import { describe, expect, test, vi } from 'vitest';
import { createdOn, distinctOwners, formatCpu, formatMemory, runBulk, suspendableUsers } from './adminRows';

describe('row formatting', () => {
  test('usage that could not be read is a dash, not zero', () => {
    expect(formatCpu(undefined)).toBe('—');
    expect(formatMemory(undefined)).toBe('—');
    expect(formatCpu(0.456)).toBe('0.46');
    expect(formatMemory(310 * 1024 * 1024)).toBe('310 MiB');
  });

  test('a created time may come as text or as an object, and may be missing', () => {
    expect(createdOn('2026-11-01T10:00:00Z')).toMatch(/2026/);
    expect(createdOn({ time: '2026-11-01T10:00:00Z' })).toMatch(/2026/);
    expect(createdOn(undefined)).toBe('—');
    expect(createdOn('not a date')).toBe('—');
  });
});

describe('bulk selections', () => {
  test('platform staff accounts are never offered for suspension', () => {
    const users = [{ id: 'a', role: 'user' }, { id: 'b', role: 'platform_admin' }, { id: 'c', role: 'platform_viewer' }];
    expect(suspendableUsers(users).map((u) => u.id)).toEqual(['a']);
  });

  test('projects of one owner suspend that owner once', () => {
    const projects = [{ ownerId: 'o1' }, { ownerId: 'o2' }, { ownerId: 'o1' }, { ownerId: undefined }];
    expect(distinctOwners(projects)).toEqual(['o1', 'o2']);
  });
});

describe('runBulk', () => {
  test('runs one at a time, in order, and counts what went through', async () => {
    const order: string[] = [];
    const result = await runBulk(['a', 'b', 'c'], async (item) => { order.push(item); });
    expect(order).toEqual(['a', 'b', 'c']);
    expect(result).toEqual({ done: 3, failed: [] });
  });

  test('one failure is reported with its reason and the rest still run', async () => {
    const act = vi.fn(async (item: string) => {
      if (item === 'b') throw { response: { data: { error: 'protected account' } } };
    });
    const result = await runBulk(['a', 'b', 'c'], act);
    expect(act).toHaveBeenCalledTimes(3);
    expect(result.done).toBe(2);
    expect(result.failed).toEqual([{ item: 'b', message: 'protected account' }]);
  });
});
