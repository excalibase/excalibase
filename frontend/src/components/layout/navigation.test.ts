import { describe, test, expect } from 'vitest';
import { PROJECT_NAV } from './navigation';

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
});
