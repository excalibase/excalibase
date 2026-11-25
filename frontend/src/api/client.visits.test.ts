import { describe, test, expect, beforeEach } from 'vitest';
import { api } from './client';

const visitKeys = () => Object.keys(localStorage).filter((key) => key.startsWith('studio.recentlyVisited'));

type Rejected = (error: unknown) => Promise<unknown>;
const onError = (): Rejected => (api.interceptors.response as unknown as { handlers: { rejected: Rejected }[] }).handlers[0].rejected;

beforeEach(() => {
  localStorage.clear();
  globalThis.history.pushState({}, '', '/login');
});

describe('a session that expired (401)', () => {
  test('forgets the profile and the visited pages, so the next person does not see them', async () => {
    localStorage.setItem('auth_user', '{"id":"u1"}');
    localStorage.setItem('studio.recentlyVisited.u1', '["/account/tokens"]');
    await expect(onError()({ response: { status: 401, headers: {} } })).rejects.toBeDefined();
    expect(localStorage.getItem('auth_user')).toBeNull();
    expect(visitKeys()).toEqual([]);
  });

  test('a function\'s own 401 changes nothing', async () => {
    localStorage.setItem('studio.recentlyVisited.u1', '["/orgs"]');
    await expect(onError()({ response: { status: 401, headers: { 'x-excalibase-function-response': '1' } } })).rejects.toBeDefined();
    expect(visitKeys()).toEqual(['studio.recentlyVisited.u1']);
  });
});
