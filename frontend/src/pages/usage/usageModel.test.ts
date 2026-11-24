import { describe, test, expect } from 'vitest';
import { parseGiB, orgUsage } from './usageModel';
import type { TierConfig } from '../../api/tiers';
import type { DatabaseInstance } from '../../types';

const tier = (extra: Partial<TierConfig> = {}) => ({ tier: 'FREE', maxProjects: 1, maxApps: 2, storageSize: '5Gi', maxStorageSize: '5Gi', cpu: '0.5', memory: '512Mi', ...extra }) as TierConfig;
const proj = (projectId: string, extra: object = {}) => ({ projectId, orgId: 'o', status: 'ACTIVE', ...extra }) as unknown as DatabaseInstance;

describe('parseGiB', () => {
  test('reads Gi, Ti, Mi and a bare number as bytes-free units, rejects junk', () => {
    expect(parseGiB('5Gi')).toBe(5);
    expect(parseGiB('2Ti')).toBe(2048);
    expect(parseGiB('512Mi')).toBe(0.5);
    expect(parseGiB('')).toBeNull();
    expect(parseGiB(undefined)).toBeNull();
    expect(parseGiB('lots')).toBeNull();
  });
});

describe('orgUsage', () => {
  test('counts live projects against the plan, ignoring deleted ones', () => {
    const usage = orgUsage([proj('a'), proj('b', { status: 'DELETING' }), proj('c', { noDatabase: true })], tier({ maxProjects: 3 }), new Map());
    expect(usage.projects).toEqual({ used: 2, limit: 3 });
    expect(usage.databases).toBe(1);
  });

  test('a limit of 0 means unlimited', () => {
    expect(orgUsage([proj('a')], tier({ maxProjects: 0 }), new Map()).projects.limit).toBeNull();
  });

  test('storage is summed from the sizes the projects report, and absent when none report one', () => {
    const withSize = orgUsage([proj('a', { storageSize: '5Gi' }), proj('b', { storageSize: '10Gi' })], tier(), new Map());
    expect(withSize.storageGiB).toBe(15);
    expect(orgUsage([proj('a')], tier(), new Map()).storageGiB).toBeNull();
  });

  test('apps per project come from the loaded lists; an unloaded list stays unknown', () => {
    const usage = orgUsage([proj('a'), proj('b')], tier(), new Map([['a', { apps: [{}, {}] as never }]]));
    expect(usage.perProject).toEqual([
      { projectId: 'a', name: 'a', apps: 2, storageGiB: null },
      { projectId: 'b', name: 'b', apps: null, storageGiB: null },
    ]);
  });

  test('no tier list yet gives no limits, never zero', () => {
    const usage = orgUsage([proj('a')], undefined, new Map());
    expect(usage.projects).toEqual({ used: 1, limit: null });
    expect(usage.limitsKnown).toBe(false);
  });
});
