import { describe, test, expect, beforeEach, vi } from 'vitest';
import { api } from './client';
import {
  listMyOrgs, getOrg, createOrg, updateOrg, deleteOrg, listOrgMembers, inviteOrgMember,
  updateOrgMemberRole, removeOrgMember, listProjectMembers, addProjectMember, listPendingInvites,
} from './orgs';

vi.mock('./client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

describe('orgs api', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: ['got'] } as never);
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'pending', inviteLink: '/register?invite=t' } } as never);
    vi.mocked(api.patch).mockResolvedValue({ data: { id: 'o1' } } as never);
    vi.mocked(api.delete).mockResolvedValue({ data: null } as never);
  });

  test('reads hit the org endpoints and return the body', async () => {
    expect(await listMyOrgs()).toEqual(['got']);
    expect(await getOrg('o1')).toEqual(['got']);
    expect(await listOrgMembers('o1')).toEqual(['got']);
    expect(await listProjectMembers('o1', 'p1')).toEqual(['got']);
    expect(await listPendingInvites('o1')).toEqual(['got']);
    expect(vi.mocked(api.get).mock.calls.map((c) => c[0])).toEqual([
      '/orgs', '/orgs/o1', '/orgs/o1/members', '/orgs/o1/projects/p1/members', '/orgs/o1/invites',
    ]);
  });

  test('inviting returns the one-time link the server issued', async () => {
    expect(await inviteOrgMember('o1', 'a@x.test', 'viewer')).toEqual({ status: 'pending', inviteLink: '/register?invite=t' });
    expect(api.post).toHaveBeenCalledWith('/orgs/o1/members', { email: 'a@x.test', role: 'viewer' });
  });

  test('writes hit the org endpoints', async () => {
    await createOrg('Org', 'org');
    await addProjectMember('o1', 'p1', 'u1', 'developer');
    expect(await updateOrg('o1', { name: 'n' })).toEqual({ id: 'o1' });
    await updateOrgMemberRole('o1', 'u1', 'admin');
    await removeOrgMember('o1', 'u1');
    await deleteOrg('o1');
    expect(api.post).toHaveBeenCalledWith('/orgs', { name: 'Org', slug: 'org' });
    expect(api.post).toHaveBeenCalledWith('/orgs/o1/projects/p1/members', { userId: 'u1', role: 'developer' });
    expect(api.patch).toHaveBeenCalledWith('/orgs/o1/members/u1', { role: 'admin' });
    expect(api.delete).toHaveBeenCalledWith('/orgs/o1/members/u1');
    expect(api.delete).toHaveBeenCalledWith('/orgs/o1');
  });
});
