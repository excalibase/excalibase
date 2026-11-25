import { describe, test, expect, beforeEach, vi } from 'vitest';
import { useAuthStore } from './auth-store';
import { loadVisits, recordVisit } from '../pages/home/recentlyVisited';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { post: vi.fn().mockResolvedValue({}) } }));

const visitKeys = () => Object.keys(localStorage).filter((key) => key.startsWith('studio.recentlyVisited'));

beforeEach(() => {
  localStorage.clear();
  useAuthStore.setState({ user: { id: 'u1', username: 'a', email: 'a@example.test', role: 'user' }, isAuthenticated: true });
});

describe('recently visited and signing out', () => {
  test('clearing the session forgets the pages this browser remembered', () => {
    recordVisit('/project/p1');
    expect(loadVisits()).toEqual(['/project/p1']);
    useAuthStore.getState().clearAuth();
    expect(visitKeys()).toEqual([]);
  });

  test('logout forgets them too, even when the server call fails', async () => {
    recordVisit('/project/p1');
    vi.mocked(api.post).mockRejectedValueOnce(new Error('offline'));
    await useAuthStore.getState().logout();
    expect(visitKeys()).toEqual([]);
  });
});
