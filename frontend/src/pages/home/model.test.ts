import { describe, test, expect } from 'vitest';
import {
  attentionItems, healthCounts, securitySummary, containsLabel, projectStatus, rowAction, servicesOf, lastUsedAt, relativeTime, type TokenInfo,
} from './model';
import type { DatabaseInstance } from '../../types';

const NOW = new Date('2026-11-21T12:00:00Z');
const day = (n: number) => new Date(NOW.getTime() + n * 86_400_000).toISOString();

function project(projectId: string, extra: Partial<DatabaseInstance> = {}): DatabaseInstance {
  return { projectId, orgId: 'org-1', databaseType: 'POSTGRESQL', tier: 'FREE', status: 'ACTIVE', currentStage: 'COMPLETED', createdAt: day(-30), ...extra } as DatabaseInstance;
}
const token = (name: string, extra: Partial<TokenInfo> = {}): TokenInfo => ({ id: name, name, ...extra });

describe('attentionItems', () => {
  test('is empty when nothing needs attention', () => {
    expect(attentionItems({ projects: [project('a')], tokens: [token('ok', { expiresAt: day(90) })], apps: [], now: NOW })).toEqual([]);
  });

  test('an expired token that reaches every project is listed with a link to revoke it', () => {
    const [item] = attentionItems({ projects: [], tokens: [token('ci', { expiresAt: day(-3) })], apps: [], now: NOW });
    expect(item).toMatchObject({ kind: 'token-expired', subject: 'ci' });
    expect(item.after).toMatch(/expired on/);
    expect(item.after).toMatch(/all your projects/);
    expect(item.actions).toEqual([{ label: 'Revoke', to: '/account/tokens' }]);
  });

  test('a token that expires within 14 days is listed, later ones are not', () => {
    const items = attentionItems({ projects: [], tokens: [token('soon', { expiresAt: day(10) }), token('later', { expiresAt: day(40) }), token('never', { expiresAt: null })], apps: [], now: NOW });
    expect(items.map((i) => i.subject)).toEqual(['soon']);
    expect(items[0].kind).toBe('token-expiring');
  });

  test('a paused project offers Resume and Delete, with the reason', () => {
    const [item] = attentionItems({ projects: [project('p1', { projectName: 'Reading list', status: 'PAUSED', pauseReason: 'idle_7d', lastActiveAt: day(-5) })], tokens: [], apps: [], now: NOW });
    expect(item).toMatchObject({ kind: 'project-paused', subject: 'Reading list' });
    expect(item.after).toContain('(idle)');
    expect(item.actions).toEqual([{ label: 'Resume', resumeProjectId: 'p1' }, { label: 'Delete', to: '/project/p1/settings' }]);
  });

  test('a failed project and a failing app are listed; paused and deleting projects are not mixed up', () => {
    const items = attentionItems({
      projects: [project('bad', { status: 'FAILED' }), project('gone', { status: 'PENDING_DELETION' })],
      tokens: [], apps: [{ projectId: 'ok', appId: 'a1', name: 'api', status: 'FAILED' }], now: NOW,
    });
    expect(items.map((i) => i.kind)).toEqual(['project-failed', 'app-failed']);
    expect(items[1].actions[0].to).toBe('/project/ok/containers/a1');
  });

  test('failures come first, then expired tokens, paused projects, expiring tokens', () => {
    const items = attentionItems({
      projects: [project('p', { status: 'PAUSED' }), project('f', { status: 'FAILED' })],
      tokens: [token('e', { expiresAt: day(-1) }), token('s', { expiresAt: day(2) })], apps: [], now: NOW,
    });
    expect(items.map((i) => i.kind)).toEqual(['project-failed', 'token-expired', 'project-paused', 'token-expiring']);
  });
});

describe('healthCounts', () => {
  test('counts running, paused, failing and in progress', () => {
    const counts = healthCounts([
      project('a'), project('b'), project('c', { status: 'PAUSED' }), project('d', { status: 'FAILED' }),
      project('e', { status: 'RESUMING' }), project('f', { status: 'PENDING_DELETION' }), project('g', { status: 'PROVISIONING', currentStage: 'WAITING_FOR_READY' }),
    ]);
    expect(counts).toEqual({ running: 2, paused: 1, failing: 1, inProgress: 2 });
  });
});

describe('securitySummary', () => {
  test('counts tokens with access to every project and tokens expiring in 60 days, not expired ones', () => {
    const summary = securitySummary({
      tokens: [
        token('all', { expiresAt: day(20) }), token('scoped', { projectId: 'p1', expiresAt: day(20) }),
        token('old', { expiresAt: day(-1) }), token('forever'), token('far', { expiresAt: day(200) }),
      ],
      openDatabases: 2, now: NOW,
    });
    expect(summary).toEqual({ openDatabases: 2, allProjectTokens: 3, expiringTokens: 2 });
  });
});

describe('Projects list helpers', () => {
  test('services are read from loaded lists; unknown stays unknown', () => {
    expect(servicesOf(undefined, undefined)).toEqual({ apps: null, sites: null, failed: [] });
    expect(servicesOf([{}, {}], [])).toEqual({ apps: 2, sites: 0, failed: [] });
    expect(servicesOf(undefined, [], ['apps'])).toEqual({ apps: null, sites: 0, failed: ['apps'] });
  });

  test('a list that failed is named, not read as none or as loading', () => {
    expect(containsLabel(project('a', { postgresVersion: '16' }), servicesOf(undefined, [], ['apps']))).toBe('Postgres 16 + apps unavailable');
    expect(containsLabel(project('a', { noDatabase: true }), servicesOf(undefined, [], ['apps']))).toBe('apps unavailable');
  });

  test('contains names the database and counts the apps and sites', () => {
    expect(containsLabel(project('a', { postgresVersion: '16' }), { apps: 1, sites: 0 })).toBe('Postgres 16 + 1 app');
    expect(containsLabel(project('a', { databaseType: 'MYSQL' }), { apps: 2, sites: 1 })).toBe('MySQL + 2 apps + 1 site');
    expect(containsLabel(project('a', { noDatabase: true }), { apps: 0, sites: 1 })).toBe('1 site');
    expect(containsLabel(project('a', { noDatabase: true }), { apps: 0, sites: 0 })).toBe('Empty project');
    expect(containsLabel(project('a', { noDatabase: true }), { apps: null, sites: null })).toBe('…');
    expect(containsLabel(project('a'), { apps: null, sites: null })).toBe('Postgres');
  });

  test('status wording and tone', () => {
    const none = { apps: 0, sites: 0 };
    expect(projectStatus(project('a'), none)).toMatchObject({ key: 'running', label: 'Running', tone: 'green' });
    expect(projectStatus(project('a', { status: 'PAUSED', pauseReason: 'idle_7d' }), none)).toMatchObject({ key: 'paused', label: 'Paused · idle', tone: 'amber' });
    expect(projectStatus(project('a', { status: 'PAUSED', pauseReason: 'manual' }), none).label).toBe('Paused · manual');
    expect(projectStatus(project('a', { status: 'PAUSED' }), none).label).toBe('Paused');
    expect(projectStatus(project('a', { noDatabase: true }), none)).toMatchObject({ key: 'empty', label: 'No services yet', tone: 'neutral' });
    expect(projectStatus(project('a', { status: 'FAILED' }), none)).toMatchObject({ key: 'failed', tone: 'red' });
    expect(projectStatus(project('a', { status: 'RESUMING' }), none).key).toBe('working');
    expect(projectStatus(project('a', { status: 'PENDING_DELETION' }), none).key).toBe('deleting');
  });

  test('the row action follows the status', () => {
    const none = { apps: 0, sites: 0 };
    const act = (p: DatabaseInstance) => rowAction(p, projectStatus(p, none));
    expect(act(project('a'))).toEqual({ label: 'Open', to: '/project/a' });
    expect(act(project('a', { status: 'PAUSED' }))).toEqual({ label: 'Resume', resumeProjectId: 'a' });
    expect(act(project('a', { noDatabase: true }))).toEqual({ label: 'Add service', to: '/project/a' });
  });

  test('last used prefers what the platform saw, and reads relatively', () => {
    expect(lastUsedAt(project('a', { lastSeenAt: day(-1), lastActiveAt: day(-9) }))).toBe(Date.parse(day(-1)));
    expect(lastUsedAt(project('a', { lastActiveAt: day(-9) }))).toBe(Date.parse(day(-9)));
    expect(lastUsedAt(project('a'))).toBeNull();
    expect(relativeTime(day(-3), NOW)).toBe('3 days ago');
    expect(relativeTime(day(0), NOW)).toBe('just now');
    expect(relativeTime('nope', NOW)).toBe('');
  });
});

describe('last used reads the server clock correctly', () => {
  test('a project active seconds ago shows just now, in any browser zone', () => {
    const justNow = new Date(NOW.getTime() - 5_000).toISOString();
    expect(relativeTime(lastUsedAt(project('a', { lastActiveAt: justNow })) as number, NOW)).toBe('just now');
  });

  test('a zoneless time is unknown, never read as UTC or local', () => {
    const zoneless = new Date(NOW.getTime() - 5_000).toISOString().replace('Z', '');
    expect(lastUsedAt(project('a', { lastActiveAt: zoneless }))).toBeNull();
    expect(relativeTime(zoneless, NOW)).toBe('');
  });
});
