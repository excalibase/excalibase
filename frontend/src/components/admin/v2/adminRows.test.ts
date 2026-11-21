import { describe, expect, test, vi } from 'vitest';
import { revokeOrgCascade } from '../../../hooks/useAdmin';
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
    expect(result).toEqual({ done: 3, failed: [], partial: [] });
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

describe('runBulk with partly done items', () => {
  test('an item the action answers as partly failed is listed, not counted as clean', async () => {
    const result = await runBulk(['a', 'b'], async (item) => ({ item }), (answer) => (answer.item === 'b' ? 'proxy: cluster busy' : undefined));
    expect(result.done).toBe(2);
    expect(result.partial).toEqual([{ item: 'b', message: 'proxy: cluster busy' }]);
  });
});

describe('revokeOrgCascade', () => {
  test('a 207 that lists failed projects is a failure naming them', async () => {
    const { api } = await import('../../../api/client');
    vi.spyOn(api, 'delete').mockResolvedValue({ status: 207, data: { orgId: 'o1', dropped: 1, failed: ['p-2', 'p-3'] } });
    await expect(revokeOrgCascade('o1', 'abuse')).rejects.toThrow(/p-2, p-3/);
  });

  test('a clean answer is a success', async () => {
    const { api } = await import('../../../api/client');
    vi.spyOn(api, 'delete').mockResolvedValue({ status: 200, data: { orgId: 'o1', dropped: 2, failed: [] } });
    await expect(revokeOrgCascade('o1', 'abuse')).resolves.toBeUndefined();
  });
});
