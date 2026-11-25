import type { DatabaseInstance } from '../../types';
import { parseInstant } from '../../utils/instant';

// What Home and the Projects list work out from the data the Studio already loads.
// Nothing here is estimated: a number that cannot be known is left out by the caller.

const DAY_MS = 86_400_000;
const EXPIRING_SOON_DAYS = 14;
const SECURITY_WINDOW_DAYS = 60;
const DELETION_STATES = new Set(['PENDING_DELETION', 'DELETING', 'DEPROVISIONED', 'BACKUPS_PENDING_DELETE']);

export interface TokenInfo {
  id: string;
  name: string;
  projectId?: string;
  expiresAt?: string | null;
}

export interface AppInfo {
  projectId: string;
  appId: string;
  name: string;
  status: string;
}

export interface AttentionAction {
  label: string;
  to?: string;
  resumeProjectId?: string;
}

export type AttentionKind = 'project-failed' | 'app-failed' | 'token-expired' | 'project-paused' | 'token-expiring';

export interface AttentionItem {
  id: string;
  kind: AttentionKind;
  before: string;
  subject: string;
  after: string;
  actions: AttentionAction[];
}

const KIND_ORDER: AttentionKind[] = ['project-failed', 'app-failed', 'token-expired', 'project-paused', 'token-expiring'];

const nameOf = (project: Pick<DatabaseInstance, 'projectId' | 'projectName'>): string => project.projectName?.trim() || project.projectId;

function formatDate(value: string | number): string {
  return new Date(parseInstant(value)).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

function expiryOf(token: TokenInfo): number | null {
  if (!token.expiresAt) return null;
  const time = parseInstant(token.expiresAt);
  return Number.isNaN(time) ? null : time;
}

const isFailed = (project: DatabaseInstance): boolean =>
  project.status === 'FAILED' || (project.currentStage === 'FAILED' && (!project.status || project.status === 'PROVISIONING'));

const PAUSE_REASON: Record<string, string> = { idle_7d: 'idle', manual: 'manual', tier_limit: 'plan limit' };

function tokenItem(token: TokenInfo, kind: 'token-expired' | 'token-expiring', expiry: number): AttentionItem {
  const reach = token.projectId ? '' : ' It can reach all your projects.';
  return {
    id: `${kind}:${token.id}`,
    kind,
    before: 'Key ',
    subject: token.name,
    after: kind === 'token-expired' ? ` expired on ${formatDate(expiry)}.${reach}` : ` expires on ${formatDate(expiry)}.${reach}`,
    actions: [{ label: kind === 'token-expired' ? 'Revoke' : 'Review', to: '/account/tokens' }],
  };
}

function pausedItem(project: DatabaseInstance): AttentionItem {
  const reason = PAUSE_REASON[project.pauseReason ?? ''];
  const since = project.lastActiveAt ?? project.updatedAt;
  const pausedSince = since ? ` since ${formatDate(since)}` : '';
  const pausedReason = reason ? ` (${reason})` : '';
  return {
    id: `project-paused:${project.projectId}`,
    kind: 'project-paused',
    before: '',
    subject: nameOf(project),
    after: ` has been paused${pausedSince}${pausedReason}.`,
    actions: [{ label: 'Resume', resumeProjectId: project.projectId }, { label: 'Delete', to: `/project/${project.projectId}/settings` }],
  };
}

export function attentionItems(input: { projects: readonly DatabaseInstance[]; tokens: readonly TokenInfo[]; apps: readonly AppInfo[]; now: Date }): AttentionItem[] {
  const { projects, tokens, apps, now } = input;
  const items: AttentionItem[] = [];
  for (const project of projects) {
    if (isFailed(project)) {
      items.push({ id: `project-failed:${project.projectId}`, kind: 'project-failed', before: '', subject: nameOf(project), after: ' did not finish building.', actions: [{ label: 'See why', to: `/project/${project.projectId}` }] });
    } else if (project.status === 'PAUSED') {
      items.push(pausedItem(project));
    }
  }
  for (const app of apps) {
    if (app.status === 'FAILED') {
      items.push({ id: `app-failed:${app.projectId}:${app.appId}`, kind: 'app-failed', before: 'App ', subject: app.name, after: ' is failing.', actions: [{ label: 'Open', to: `/project/${app.projectId}/containers/${app.appId}` }] });
    }
  }
  for (const token of tokens) {
    const expiry = expiryOf(token);
    if (expiry === null) continue;
    if (expiry <= now.getTime()) items.push(tokenItem(token, 'token-expired', expiry));
    else if (expiry - now.getTime() <= EXPIRING_SOON_DAYS * DAY_MS) items.push(tokenItem(token, 'token-expiring', expiry));
  }
  return items.sort((a, b) => KIND_ORDER.indexOf(a.kind) - KIND_ORDER.indexOf(b.kind));
}

export interface HealthCounts { running: number; paused: number; failing: number; inProgress: number }

export function healthCounts(projects: readonly DatabaseInstance[]): HealthCounts {
  const counts: HealthCounts = { running: 0, paused: 0, failing: 0, inProgress: 0 };
  for (const project of projects) {
    if (DELETION_STATES.has(project.status)) continue;
    if (isFailed(project)) counts.failing++;
    else if (project.status === 'ACTIVE') counts.running++;
    else if (project.status === 'PAUSED') counts.paused++;
    else counts.inProgress++;
  }
  return counts;
}

export interface SecuritySummary { openDatabases: number; allProjectTokens: number; expiringTokens: number }

export function securitySummary(input: { tokens: readonly TokenInfo[]; openDatabases: number; now: Date }): SecuritySummary {
  const live = input.tokens.filter((token) => {
    const expiry = expiryOf(token);
    return expiry === null || expiry > input.now.getTime();
  });
  return {
    openDatabases: input.openDatabases,
    allProjectTokens: live.filter((token) => !token.projectId).length,
    expiringTokens: live.filter((token) => {
      const expiry = expiryOf(token);
      return expiry !== null && expiry - input.now.getTime() <= SECURITY_WINDOW_DAYS * DAY_MS;
    }).length,
  };
}

// The Projects list.

// null: not loaded (yet) or not readable, which is not the same as none.
// `failed` names a list that could not be read, which is not the same as none.
export interface ProjectServices { apps: number | null; sites: number | null; failed?: readonly ('apps' | 'sites')[] }

export function servicesOf(apps: readonly unknown[] | undefined, sites: readonly unknown[] | undefined, failed: readonly ('apps' | 'sites')[] = []): ProjectServices {
  return { apps: apps ? apps.length : null, sites: sites ? sites.length : null, failed };
}

function databaseLabel(project: DatabaseInstance): string | null {
  if (project.noDatabase === true) return null;
  if (project.documentDb === true) return 'DocumentDB';
  if (project.databaseType === 'MYSQL') return ['MySQL', project.mysqlVersion].filter(Boolean).join(' ');
  if (project.databaseType === 'POSTGRESQL') return ['Postgres', project.postgresVersion].filter(Boolean).join(' ');
  return String(project.databaseType);
}

const count = (n: number, noun: string) => `${n} ${noun}${n === 1 ? '' : 's'}`;

export function containsLabel(project: DatabaseInstance, services: ProjectServices): string {
  const parts: string[] = [];
  const database = databaseLabel(project);
  if (database) parts.push(database);
  if (services.apps) parts.push(count(services.apps, 'app'));
  if (services.sites) parts.push(count(services.sites, 'site'));
  for (const list of services.failed ?? []) parts.push(`${list} unavailable`);
  if (parts.length > 0) return parts.join(' + ');
  return services.apps === null || services.sites === null ? '…' : 'Empty project';
}

export type StatusKey = 'running' | 'paused' | 'empty' | 'failed' | 'working' | 'deleting';
export type Tone = 'green' | 'amber' | 'red' | 'blue' | 'neutral';
export interface ProjectStatus { key: StatusKey; label: string; tone: Tone }

const WORKING_LABEL: Record<string, string> = { PAUSING: 'Pausing', RESUMING: 'Resuming', UPGRADING: 'Upgrading', RESTORING: 'Restoring', PROVISIONING: 'Setting up' };

export function projectStatus(project: DatabaseInstance, services: ProjectServices): ProjectStatus {
  if (DELETION_STATES.has(project.status)) {
    return { key: 'deleting', label: project.status === 'PENDING_DELETION' ? 'Scheduled for deletion' : 'Deleting', tone: 'red' };
  }
  if (isFailed(project)) return { key: 'failed', label: 'Failed', tone: 'red' };
  if (project.status === 'PAUSED') {
    const reason = PAUSE_REASON[project.pauseReason ?? ''];
    return { key: 'paused', label: reason ? `Paused · ${reason}` : 'Paused', tone: 'amber' };
  }
  if (project.status === 'ACTIVE') {
    const empty = project.noDatabase === true && services.apps === 0 && services.sites === 0;
    return empty ? { key: 'empty', label: 'No services yet', tone: 'neutral' } : { key: 'running', label: 'Running', tone: 'green' };
  }
  return { key: 'working', label: WORKING_LABEL[project.status] ?? 'In progress', tone: 'blue' };
}

export function rowAction(project: DatabaseInstance, status: ProjectStatus): AttentionAction {
  const to = `/project/${project.projectId}`;
  if (status.key === 'paused') return { label: 'Resume', resumeProjectId: project.projectId };
  if (status.key === 'empty') return { label: 'Add service', to };
  return { label: 'Open', to };
}

export function lastUsedAt(project: DatabaseInstance): number | null {
  const time = parseInstant(project.lastSeenAt ?? project.lastActiveAt);
  return Number.isNaN(time) ? null : time;
}

const RELATIVE = new Intl.RelativeTimeFormat('en', { numeric: 'always' });
const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [['year', 365 * DAY_MS], ['month', 30 * DAY_MS], ['day', DAY_MS], ['hour', 3_600_000], ['minute', 60_000]];

export function relativeTime(value: string | number, now: Date): string {
  const time = parseInstant(value);
  if (Number.isNaN(time)) return '';
  const elapsed = now.getTime() - time;
  for (const [unit, size] of UNITS) {
    if (elapsed >= size) return RELATIVE.format(-Math.floor(elapsed / size), unit);
  }
  return 'just now';
}
