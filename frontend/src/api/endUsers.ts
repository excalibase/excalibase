import { api } from './client';

// A project's end users as excalibase-auth stores them (EXC-370). The control
// plane relays these calls with its own short-lived token; only project
// admins may make them, and every role change is audited.
export interface EndUser {
  id: number;
  email: string;
  role: string;
  allowedRoles: string[];
}

export interface EndUserPage {
  users: EndUser[];
  total?: number;
}

export interface EndUserRoleChange {
  role: string;
  // Omitted, auth allows only `role`.
  allowedRoles?: string[];
}

// Codes the control plane answers with itself; auth's own 4xx codes pass
// through unchanged.
export type EndUserErrorCode =
  | 'invalid_request'
  | 'auth_unavailable'
  | 'auth_rejected_credential'
  | 'auth_not_configured'
  | 'internal_error';

// Mirrors the server's shape check so a typo is caught before sending.
export const END_USER_ROLE_PATTERN = /^[a-z][a-z0-9_]{0,62}$/;

const endUsersPath = (projectId: string) =>
  `/projects/${encodeURIComponent(projectId)}/end-users/`;

export async function listEndUsers(
  projectId: string,
  page: { limit?: number; offset?: number } = {},
): Promise<EndUserPage> {
  const params: Record<string, number> = {};
  if (page.limit !== undefined) params.limit = page.limit;
  if (page.offset !== undefined) params.offset = page.offset;
  return (await api.get<EndUserPage>(endUsersPath(projectId), { params })).data;
}

export async function setEndUserRole(
  projectId: string,
  userId: number,
  change: EndUserRoleChange,
): Promise<EndUser> {
  return (await api.put<EndUser>(`${endUsersPath(projectId)}${userId}/role`, change)).data;
}
