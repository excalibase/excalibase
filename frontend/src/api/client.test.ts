import { describe, test, expect, beforeEach, vi } from 'vitest';
import type { InternalAxiosRequestConfig } from 'axios';

describe('Studio API client', () => {
  beforeEach(() => {
    vi.resetModules();
    localStorage.clear();
  });

  test('never attaches a stored token: the httpOnly cookie is the only credential', async () => {
    localStorage.setItem('auth_token', 'stale-token');
    const { api } = await import('./client');
    let sent: InternalAxiosRequestConfig | undefined;
    await api.get('/auth/me', {
      adapter: async (config) => {
        sent = config;
        return { data: {}, status: 200, statusText: 'OK', headers: {}, config };
      },
    });
    expect(sent?.headers.Authorization).toBeUndefined();
    expect(sent?.withCredentials).toBe(true);
  });

  // EXC-555: invoking a user's function that answers 401 signed the
  // developer out of Studio. Only Studio's own 401 means the session ended.
  async function answer401(headers: Record<string, string>) {
    const { api } = await import('./client');
    localStorage.setItem('auth_user', '{"id":"u1"}');
    window.history.pushState({}, '', '/project/p1/edge-functions');
    await api
      .post('/projects/p1/functions/deny/invoke', '', {
        adapter: async (config) =>
          Promise.reject(Object.assign(new Error('401'), {
            config,
            response: { data: 'nope', status: 401, statusText: '', headers, config },
          })),
      })
      .catch(() => undefined);
  }

  test("a function's own 401 leaves the Studio session alone", async () => {
    await answer401({ 'x-excalibase-function-response': '1' });
    expect(localStorage.getItem('auth_user')).not.toBeNull();
    expect(window.location.pathname).toBe('/project/p1/edge-functions');
  });

  test("Studio's own 401 still ends the session", async () => {
    await answer401({});
    expect(localStorage.getItem('auth_user')).toBeNull();
  });

  test('a token left in storage by an older Studio is purged on load', async () => {
    localStorage.setItem('auth_token', 'stale-token');
    await import('../stores/auth-store');
    expect(localStorage.getItem('auth_token')).toBeNull();
  });
});
