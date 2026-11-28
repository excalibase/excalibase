import { api } from './client';

// The admin console's wire types (EXC-757). They mirror internal/adminconsole.

export type AccountStatus = 'active' | 'suspended' | 'disabled';

export interface ConsoleUser {
  id: string;
  email: string;
  username: string;
  // Absent when the platform has no disposable-address list: unknown, not "no".
  tempMail?: boolean;
  emailVerified: boolean;
  signedUpAt?: string;
  lastLoginAt?: string;
  lastLoginIp?: string;
  role: string;
  status: AccountStatus;
  suspendedReason?: string;
  suspendedAt?: string;
  orgCount: number;
  projectCount: number;
  runningApps: number;
}

export interface ConsoleOrg {
  id: string;
  name: string;
  slug: string;
  tier: string;
  ownerId: string;
  ownerEmail: string;
  ownerStatus: AccountStatus;
  projectCount: number;
  runningApps: number;
  cpuCores: number;
  memoryMb: number;
  createdAt?: string;
  // The org's credit balance in micro-dollars; absent while credits are off or the org has no account.
  creditMicros?: number;
  // What the org holds of its plan; absent while the plan budget is off.
  budget?: { cpuMilliUsed: number; cpuMilliLimit: number; memoryBytesUsed: number; memoryBytesLimit: number };
}

export interface Page<T> {
  items: T[];
  total: number;
}

export interface ProjectBrief {
  projectId: string;
  projectName: string;
  orgId: string;
  ownerEmail: string;
  ownerStatus: AccountStatus;
  status: string;
  failureReason?: string;
  cpuCores?: number;
  memoryMb?: number;
  egressBytesPerSec?: number;
}

export type DomainCount = { domain: string; count: number };

export interface Overview {
  newlySuspended: { userId: string; email: string; reason: string; suspendedAt: string }[];
  signups24h: number;
  signupDomains: DomainCount[];
  failedProjects: ProjectBrief[];
  topCpu: ProjectBrief[];
  topMemory: ProjectBrief[];
  topEgress: ProjectBrief[];
}

export interface AppView {
  id: string;
  name: string;
  image: string;
  replicas: number;
  status: string;
  // What the runtime answered just now; null when it could not be asked.
  runningReplicas: number | null;
  cpuMilliPerReplica: number;
  memoryMibPerReplica: number;
  tier: string;
  internal: boolean;
}

export interface ProjectDetail {
  projectId: string;
  projectName: string;
  orgId: string;
  orgName: string;
  orgSlug: string;
  tier: string;
  ownerId: string;
  ownerEmail: string;
  ownerStatus: AccountStatus;
  ownerSuspendedReason?: string;
  databaseType: string;
  databaseVersion: string;
  databaseStatus: string;
  noDatabase: boolean;
  failureReason?: string;
  cpuCores: number;
  memoryMb: number;
  apps: AppView[];
  sites: { id: string; name: string; hostname: string }[];
  functions: { id: string; active: boolean }[];
  audit: { action: string; userId?: string; details?: string; timestamp?: string }[];
}

export interface SuspendResult {
  userId: string;
  status: AccountStatus;
  appsStopped: unknown[];
  appsRestored: unknown[];
  appsFailed: { appName: string; reason: string }[];
}

export interface ListQuery {
  q?: string;
  includeSuspended?: boolean;
  limit?: number;
  offset?: number;
  // One of the columns the server allows, and its direction.
  sort?: string;
  order?: 'asc' | 'desc';
  // Users: status and role. Orgs: plan (tier) and owner status.
  status?: string;
  role?: string;
  tier?: string;
  ownerStatus?: string;
}

const params = (query: ListQuery) => ({
  q: query.q || undefined,
  includeSuspended: query.includeSuspended ? 'true' : undefined,
  limit: query.limit,
  offset: query.offset,
  sort: query.sort || undefined,
  order: query.sort ? query.order : undefined,
  status: query.status || undefined,
  role: query.role || undefined,
  tier: query.tier || undefined,
  ownerStatus: query.ownerStatus || undefined,
});

// One page of a list read by keyset: the total it belongs to and where the next page starts.
export interface CursorPage<T> {
  items: T[];
  total: number;
  nextCursor?: string;
}

export type SuspendedBrief = Overview['newlySuspended'][number];

export const ATTENTION_PAGE_SIZE = 20;

const attentionPage = async <T>(path: string, cursor: string | undefined) =>
  (await api.get<CursorPage<T>>(`/admin/attention/${path}`, { params: { limit: ATTENTION_PAGE_SIZE, ...(cursor ? { cursor } : {}) } })).data;

export const getSuspensions = (cursor?: string) => attentionPage<SuspendedBrief>('suspensions', cursor);
export const getFailedProjects = (cursor?: string) => attentionPage<ProjectBrief>('failed-projects', cursor);

export const getOverview = async () => (await api.get<Overview>('/admin/console/overview')).data;

export const listConsoleUsers = async (query: ListQuery) =>
  (await api.get<Page<ConsoleUser>>('/admin/console/users', { params: params(query) })).data;

export const listConsoleOrgs = async (query: ListQuery) =>
  (await api.get<Page<ConsoleOrg>>('/admin/console/orgs', { params: params(query) })).data;

// Opening a project's data needs a written reason, which the server audits.
export const getProjectDetail = async (projectId: string, reason: string) =>
  (await api.get<ProjectDetail>(`/admin/console/projects/${projectId}`, { params: { reason } })).data;

export const suspendUser = async (userId: string, reason: string, reasonCode?: string) =>
  (await api.post<SuspendResult>(`/admin/console/users/${userId}/suspend`, reasonCode ? { reason, reasonCode } : { reason })).data;

export const unsuspendUser = async (userId: string, reason: string) =>
  (await api.post<SuspendResult>(`/admin/console/users/${userId}/unsuspend`, { reason })).data;

// One entry of the platform audit log (EXC-856).
export interface AuditRow {
  id: string;
  at?: string;
  actorId?: string;
  actorEmail?: string;
  action: string;
  resource?: string;
  resourceId?: string;
  targetEmail?: string;
  details?: string;
  ip?: string;
}

// The log has no total: it is read by keyset, and the next page starts at nextCursor.
export interface AuditPage {
  items: AuditRow[];
  nextCursor?: string;
}

export interface AuditFilter {
  actor?: string;
  action?: string;
  target?: string;
}

export const AUDIT_PAGE_SIZE = 25;

export const listAudit = async (filter: AuditFilter, cursor?: string) =>
  (await api.get<AuditPage>('/admin/audit/', {
    params: {
      limit: AUDIT_PAGE_SIZE,
      actor: filter.actor || undefined,
      action: filter.action || undefined,
      target: filter.target || undefined,
      ...(cursor ? { cursor } : {}),
    },
  })).data;

export interface UserTrust { state: string; method: string; reason: string; decidedAt?: string }
export interface TrustChange { from?: string; to: string; method: string; reason: string; actor: string; at?: string }
export interface SuspensionEvent { action: string; actorId?: string; actorEmail?: string; reason?: string; at?: string }
export interface SignInMethod { kind: 'password' | 'provider'; provider?: string; email?: string; linkedAt?: string }

// One account's page. It carries no credential, hash or token.
export interface UserDetail {
  profile: {
    id: string; email: string; username: string; role: string; status: AccountStatus;
    tempMail?: boolean; emailVerified: boolean; emailVerifiedAt?: string; signedUpAt?: string;
    lastLoginAt?: string; lastLoginIp?: string; trust?: UserTrust;
  };
  suspension: { suspended: boolean; reason?: string; reasonCode?: string; suspendedAt?: string };
  suspensions: SuspensionEvent[];
  trustHistory: TrustChange[];
  orgs: { id: string; name: string; slug: string; tier: string; role: string; owner: boolean }[];
  projects: { projectId: string; name: string; orgId: string; orgName: string; status: string }[];
  signIn: SignInMethod[];
  audit: AuditRow[];
}

export const getUserDetail = async (userId: string) =>
  (await api.get<UserDetail>(`/admin/accounts/${encodeURIComponent(userId)}`)).data;
