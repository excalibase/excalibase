import { describe, test, expect } from 'vitest';
import { projectPages } from './CommandPalette';

const BASE = { appHosting: true, mcp: true, sites: true, postgres: true, platformStaff: true };
const labels = (pages: { label: string }[]) => pages.map((p) => p.label);

describe('project pages in the palette follow the navigation', () => {
  test('no Secrets (the vault) for anyone but platform staff', () => {
    expect(labels(projectPages('p1', { ...BASE, platformStaff: false }, false, {}))).not.toContain('Secrets');
    expect(labels(projectPages('p1', BASE, false, {}))).toContain('Secrets');
  });

  test('pages carry the v2 names, not Databases, Containers or Vault', () => {
    const pages = labels(projectPages('p1', BASE, false, {}));
    expect(pages).toContain('Database: Tables');
    expect(pages).toContain('Apps');
    for (const old of ['Databases: Tables', 'Containers', 'Vault']) expect(pages).not.toContain(old);
  });

  test('no page behind a flag that is off', () => {
    expect(labels(projectPages('p1', BASE, false, { slowqueries: false }))).not.toContain('Monitoring: Slow queries');
    expect(labels(projectPages('p1', BASE, false, { slowqueries: true }))).toContain('Monitoring: Slow queries');
  });

  test('no capability section while its capability is off', () => {
    expect(labels(projectPages('p1', { ...BASE, mcp: false }, false, {}))).not.toContain('AI Tools');
  });

  test('no Postgres-only page for a MySQL project', () => {
    const pages = labels(projectPages('p1', { ...BASE, postgres: false }, false, {}));
    expect(pages).not.toContain('Authentication: Users');
    expect(pages).not.toContain('Monitoring: Performance');
    expect(pages).toContain('Monitoring: Metrics');
  });

  test('links point under the project', () => {
    expect(projectPages('p1', BASE, false, {}).find((p) => p.label === 'Settings')?.to).toBe('/project/p1/settings');
  });
});
