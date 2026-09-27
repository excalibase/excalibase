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

  test('a token left in storage by an older Studio is purged on load', async () => {
    localStorage.setItem('auth_token', 'stale-token');
    await import('../stores/auth-store');
    expect(localStorage.getItem('auth_token')).toBeNull();
  });
});
