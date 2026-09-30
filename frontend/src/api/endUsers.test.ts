import { describe, test, expect, beforeEach, vi } from 'vitest';
import { api } from './client';
import { END_USER_ROLE_PATTERN, listEndUsers, setEndUserRole } from './endUsers';

vi.mock('./client', () => ({
  api: { get: vi.fn(), put: vi.fn() },
}));

describe('end users api', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  test('lists a page of end users through the control plane', async () => {
    const page = { users: [{ id: 3, email: 'a@x.test', role: 'user', allowedRoles: ['user'] }], total: 1 };
    vi.mocked(api.get).mockResolvedValue({ data: page } as never);

    expect(await listEndUsers('proj-a', { limit: 25, offset: 50 })).toEqual(page);
    expect(api.get).toHaveBeenCalledWith('/projects/proj-a/end-users/', { params: { limit: 25, offset: 50 } });
  });

  test('sends no paging it was not given', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { users: [] } } as never);
    await listEndUsers('proj-a');
    expect(api.get).toHaveBeenCalledWith('/projects/proj-a/end-users/', { params: {} });
  });

  test('sets a role and returns the account auth answered with', async () => {
    const user = { id: 7, email: 'b@x.test', role: 'editor', allowedRoles: ['editor', 'user'] };
    vi.mocked(api.put).mockResolvedValue({ data: user } as never);

    expect(await setEndUserRole('proj-a', 7, { role: 'editor', allowedRoles: ['editor', 'user'] })).toEqual(user);
    expect(api.put).toHaveBeenCalledWith('/projects/proj-a/end-users/7/role', {
      role: 'editor',
      allowedRoles: ['editor', 'user'],
    });
  });

  test('the role pattern matches the server', () => {
    expect(END_USER_ROLE_PATTERN.test('editor_2')).toBe(true);
    expect(END_USER_ROLE_PATTERN.test('Editor')).toBe(false);
    expect(END_USER_ROLE_PATTERN.test('2x')).toBe(false);
    expect(END_USER_ROLE_PATTERN.test('a'.repeat(64))).toBe(false);
  });
});
