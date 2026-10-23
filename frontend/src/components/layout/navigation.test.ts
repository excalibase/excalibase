import { describe, test, expect } from 'vitest';
import { PROJECT_NAV, visibleNavItems, visibleProjectNav } from './navigation';

describe('project navigation', () => {
  test('the project home is the project, not its database', () => {
    const home = PROJECT_NAV.find((section) => section.key === 'home');
    expect(home?.label).toBe('Overview');
    expect(home?.to).toBe('');
  });

  test('Databases opens on the database overview', () => {
    const databases = PROJECT_NAV.find((section) => section.key === 'database');
    expect(databases?.children?.[0]).toMatchObject({ label: 'Overview', to: 'database/overview' });
  });

  test('AI Tools opens the Connect your AI tool page', () => {
    const aiTools = PROJECT_NAV.find((section) => section.key === 'ai-tools');
    expect(aiTools).toMatchObject({ label: 'AI Tools', to: 'ai-tools' });
  });

  test('AI Tools is listed only when the server has MCP on', () => {
    const keys = (mcp: boolean) => visibleProjectNav({ appHosting: true, mcp }).map((section) => section.key);
    expect(keys(false)).not.toContain('ai-tools');
    expect(keys(true)).toContain('ai-tools');
  });

  // EXC-586: a MySQL project is backed up; snapshots and migrations stay Postgres pages.
  test('a MySQL project lists Backups and no other operation', () => {
    const operations = visibleProjectNav({ appHosting: true, mcp: false, postgres: false }).find((section) => section.key === 'operations');
    expect(operations).toBeDefined();
    expect(visibleNavItems(operations?.children ?? [], false, false).map((item) => item.label)).toEqual(['Backups']);
    expect(visibleNavItems(operations?.children ?? [], false, true).map((item) => item.label)).toEqual(['Backups', 'Snapshots', 'Migrations']);
  });

  // EXC-353: Sites follows the server's sites config, which is off unless Cloudflare is set up.
  test('Sites is listed only when the server reports sites', () => {
    const keys = (sites?: boolean) => visibleProjectNav({ appHosting: true, mcp: false, sites }).map((section) => section.key);
    expect(keys(undefined)).not.toContain('sites');
    expect(keys(false)).not.toContain('sites');
    expect(keys(true)).toContain('sites');
    expect(PROJECT_NAV.find((section) => section.key === 'sites')).toMatchObject({ label: 'Sites', to: 'sites' });
  });

  // EXC-654: Slow queries ships dark and serves Postgres and MySQL alike.
  test('Slow queries is listed only with its feature on, on either engine', () => {
    const monitoring = PROJECT_NAV.find((section) => section.key === 'monitoring');
    const labels = (postgres: boolean, slowqueries?: boolean) =>
      visibleNavItems(monitoring?.children ?? [], false, postgres, { slowqueries }).map((item) => item.label);
    expect(labels(true)).not.toContain('Slow queries');
    expect(labels(true, false)).not.toContain('Slow queries');
    expect(labels(true, true)).toContain('Slow queries');
    expect(labels(false, true)).toEqual(['Metrics', 'Alerts', 'Logs', 'Slow queries']);
    expect(monitoring?.children?.find((item) => item.label === 'Slow queries')).toMatchObject({ to: 'monitoring/slow-queries', feature: 'slowqueries' });
  });
});
