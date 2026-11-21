import { describe, test, expect, beforeEach, vi } from 'vitest';
import { loadVisits, recordVisit, describeVisit, MAX_VISITS } from './recentlyVisited';
import { clearVisits } from '../../utils/visitStorage';
import { useAuthStore } from '../../stores/auth-store';
import type { DatabaseInstance } from '../../types';

const projects = [{ projectId: 'p1', projectName: 'Reading list', databaseType: 'POSTGRESQL' }] as DatabaseInstance[];

const signIn = (id: string) => useAuthStore.getState().setAuth({ id, username: id, email: `${id}@example.test`, role: 'user' });

beforeEach(() => {
  localStorage.clear();
  signIn('u1');
});

describe('visits belong to the person signed in', () => {
  test('another person on the same browser does not see them, and their list is dropped', () => {
    recordVisit('/account/tokens');
    signIn('u2');
    expect(loadVisits()).toEqual([]);
    recordVisit('/orgs');
    expect(loadVisits()).toEqual(['/orgs']);
    expect(Object.keys(localStorage).filter((key) => key.startsWith('studio.recentlyVisited'))).toEqual(['studio.recentlyVisited.u2']);
  });

  test('a tab whose person is no longer the stored one leaves the stored person\'s list alone', () => {
    signIn('u2');
    recordVisit('/orgs');
    useAuthStore.setState({ user: { id: 'u1', username: 'u1', email: 'u1@example.test', role: 'user' }, isAuthenticated: true });
    recordVisit('/account/tokens');
    expect(localStorage.getItem('studio.recentlyVisited.u2')).toBe('["/orgs"]');
    expect(loadVisits()).toEqual(['/account/tokens']);
  });

  test('the list kept before visits were stored per person is never shown and is removed', () => {
    localStorage.setItem('studio.recentlyVisited', JSON.stringify(['/account/tokens']));
    expect(loadVisits()).toEqual([]);
    recordVisit('/orgs');
    expect(localStorage.getItem('studio.recentlyVisited')).toBeNull();
  });

  test('with nobody signed in nothing is read or kept', () => {
    useAuthStore.setState({ user: null, isAuthenticated: false });
    recordVisit('/orgs');
    expect(loadVisits()).toEqual([]);
    expect(Object.keys(localStorage).filter((key) => key.startsWith('studio.recentlyVisited'))).toEqual([]);
  });

  test('clearVisits removes every list', () => {
    recordVisit('/orgs');
    localStorage.setItem('studio.recentlyVisited.u9', '[]');
    clearVisits();
    expect(Object.keys(localStorage).filter((key) => key.startsWith('studio.recentlyVisited'))).toEqual([]);
  });
});

describe('recording visits', () => {
  test('keeps the newest first and drops a repeat', () => {
    recordVisit('/project/p1');
    recordVisit('/account/tokens');
    recordVisit('/project/p1');
    expect(loadVisits()).toEqual(['/project/p1', '/account/tokens']);
  });

  test('ignores pages that are not worth returning to and keeps only the newest few', () => {
    for (const path of ['/', '/login', '/provision']) recordVisit(path);
    expect(loadVisits()).toEqual([]);
    for (let i = 0; i < MAX_VISITS + 5; i++) recordVisit(`/project/p${i}`);
    expect(loadVisits()).toHaveLength(MAX_VISITS);
    expect(loadVisits()[0]).toBe(`/project/p${MAX_VISITS + 4}`);
  });

  test('works without browser storage', () => {
    const broken = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked'); });
    const broken2 = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked'); });
    expect(() => recordVisit('/project/p1')).not.toThrow();
    expect(loadVisits()).toEqual([]);
    broken.mockRestore();
    broken2.mockRestore();
  });

  test('a damaged value is read as empty', () => {
    localStorage.setItem('studio.recentlyVisited.u1', '{"not":"a list"}');
    expect(loadVisits()).toEqual([]);
    localStorage.setItem('studio.recentlyVisited.u1', '[1,"/project/p1"]');
    expect(loadVisits()).toEqual(['/project/p1']);
  });
});

describe('describeVisit', () => {
  test('names a project page by project and section', () => {
    expect(describeVisit('/project/p1', projects)).toEqual({ to: '/project/p1', title: 'Reading list', kind: 'Overview' });
    expect(describeVisit('/project/p1/database/tables', projects)).toEqual({ to: '/project/p1/database/tables', title: 'Reading list › Tables', kind: 'Postgres' });
    expect(describeVisit('/project/p1/containers/a7', projects)?.title).toBe('Reading list › Containers');
  });

  test('a project that is gone is dropped once projects are known, kept while they load', () => {
    expect(describeVisit('/project/zzz', projects)).toBeNull();
    expect(describeVisit('/project/zzz', undefined)?.title).toBe('zzz');
  });

  test('names the account pages', () => {
    expect(describeVisit('/account/tokens', projects)).toEqual({ to: '/account/tokens', title: 'Access keys', kind: 'Account' });
    expect(describeVisit('/usage', projects)?.title).toBe('Usage & credits');
    expect(describeVisit('/instances', projects)?.title).toBe('Projects');
    expect(describeVisit('/something-unknown', projects)).toBeNull();
  });
});
