import { describe, test, expect, beforeEach } from 'vitest';
import { useAuthStore } from './auth-store';
import { loadVisits, recordVisit } from '../pages/home/recentlyVisited';

beforeEach(() => localStorage.clear());

describe('recently visited and signing out', () => {
  test('clearing the session forgets the pages this browser remembered', () => {
    recordVisit('/project/p1');
    expect(loadVisits()).toEqual(['/project/p1']);
    useAuthStore.getState().clearAuth();
    expect(loadVisits()).toEqual([]);
  });
});
