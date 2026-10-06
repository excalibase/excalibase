import { describe, test, expect } from 'vitest';
import { PROJECT_NAV, visibleProjectNav } from './navigation';

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
});
