import { api } from './client';

export interface Org {
  id: string;
  name: string;
  slug: string;
  tier: string;
  ownerId: string;
  // The caller's membership role in this org (e.g. "owner"), included by the
  // orgs-for-user listing. Optional: absent on admin/cross-org responses.
  role?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface OrgMember {
  orgId: string;
  userId: string;
  role: string;
  email: string;
  username: string;
  createdAt?: string;
}

export interface ProjectMember {
  projectId: string;
  orgId: string;
  userId: string;
  role: string;
  email: string;
  username: string;
  createdAt?: string;
}

export async function listMyOrgs(): Promise<Org[]> {
  const { data } = await api.get<Org[]>('/orgs');
  return data;
}

export async function getOrg(orgId: string): Promise<Org> {
  const { data } = await api.get<Org>(`/orgs/${orgId}`);
  return data;
}

export async function createOrg(name: string, slug: string): Promise<Org> {
  const { data } = await api.post<Org>('/orgs', { name, slug });
  return data;
}

export async function updateOrg(orgId: string, updates: { name?: string; tier?: string }): Promise<Org> {
  const { data } = await api.patch<Org>(`/orgs/${orgId}`, updates);
  return data;
}

export async function deleteOrg(orgId: string): Promise<void> {
  await api.delete(`/orgs/${orgId}`);
}

export async function listOrgMembers(orgId: string): Promise<OrgMember[]> {
  const { data } = await api.get<OrgMember[]>(`/orgs/${orgId}/members`);
  return data;
}

export async function inviteOrgMember(orgId: string, email: string, role: string): Promise<void> {
  await api.post(`/orgs/${orgId}/members`, { email, role });
}

export async function updateOrgMemberRole(orgId: string, userId: string, role: string): Promise<void> {
  await api.patch(`/orgs/${orgId}/members/${userId}`, { role });
}

export async function removeOrgMember(orgId: string, userId: string): Promise<void> {
  await api.delete(`/orgs/${orgId}/members/${userId}`);
}

export async function listProjectMembers(orgId: string, projectId: string): Promise<ProjectMember[]> {
  const { data } = await api.get<ProjectMember[]>(`/orgs/${orgId}/projects/${projectId}/members`);
  return data;
}

export async function addProjectMember(orgId: string, projectId: string, userId: string, role: string): Promise<void> {
  await api.post(`/orgs/${orgId}/projects/${projectId}/members`, { userId, role });
}

export interface PendingInvite {
  id: number;
  orgId: string;
  email: string;
  role: string;
  invitedBy: string;
  createdAt?: string;
}

export async function listPendingInvites(orgId: string): Promise<PendingInvite[]> {
  const { data } = await api.get<PendingInvite[]>(`/orgs/${orgId}/invites`);
  return data;
}
