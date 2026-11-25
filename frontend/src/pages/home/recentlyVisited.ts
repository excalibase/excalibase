import { useAuthStore } from '../../stores/auth-store';
import { pruneVisits, visitsKey } from '../../utils/visitStorage';
import type { DatabaseInstance } from '../../types';

export const MAX_VISITS = 10;

const IGNORED = new Set(['/', '/login', '/register', '/provision']);

// Browser storage can be missing or throw (private window, blocked site data): the list then stays empty.
// With nobody signed in nothing is read or kept.
const keyOfPersonSignedIn = (): string | null => {
  const id = useAuthStore.getState().user?.id;
  return id ? visitsKey(id) : null;
};

// A tab left open after someone else signed in elsewhere must not delete that person's list:
// other lists are dropped only when the stored profile is the person this tab believes is signed in.
function storedPersonIsThisOne(): boolean {
  try {
    const stored: unknown = JSON.parse(localStorage.getItem('auth_user') ?? 'null');
    return (stored as { id?: unknown } | null)?.id === useAuthStore.getState().user?.id;
  } catch {
    return false;
  }
}

export function loadVisits(): string[] {
  const key = keyOfPersonSignedIn();
  if (!key) return [];
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(key) ?? '[]');
    return Array.isArray(parsed) ? parsed.filter((p): p is string => typeof p === 'string') : [];
  } catch {
    return [];
  }
}

export function recordVisit(path: string): void {
  const key = keyOfPersonSignedIn();
  if (!key || IGNORED.has(path)) return;
  if (storedPersonIsThisOne()) pruneVisits(key);
  const next = [path, ...loadVisits().filter((p) => p !== path)].slice(0, MAX_VISITS);
  try {
    localStorage.setItem(key, JSON.stringify(next));
  } catch {
    // Nothing is remembered in this browser.
  }
}

export interface Visit { to: string; title: string; kind: string }

const PAGES: Record<string, Visit> = {
  '/instances': { to: '/instances', title: 'Projects', kind: 'Console' },
  '/usage': { to: '/usage', title: 'Usage & credits', kind: 'Account' },
  '/account/tokens': { to: '/account/tokens', title: 'Access keys', kind: 'Account' },
  '/orgs': { to: '/orgs', title: 'Organizations', kind: 'Account' },
};

const humanize = (segment: string) => {
  const spaced = segment.replaceAll('-', ' ');
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
};

// Pages with a detail page below them are named by the list they belong to.
const DETAIL_SECTIONS = new Set(['containers', 'sites']);

const engineName = (project: DatabaseInstance) => (project.databaseType === 'MYSQL' ? 'MySQL' : 'Postgres');

const detailKind = (project: DatabaseInstance | undefined) => (project ? engineName(project) : 'Project');

// null: nothing worth a link (an unknown page, or a project that no longer exists).
// `projects` undefined means they have not loaded yet, so a project is kept by its id.
export function describeVisit(path: string, projects: readonly DatabaseInstance[] | undefined): Visit | null {
  if (PAGES[path]) return PAGES[path];
  const match = /^\/project\/([^/]+)(?:\/(.*))?$/.exec(path);
  if (!match) return null;
  const [, projectId, rest = ''] = match;
  const project = projects?.find((p) => p.projectId === projectId);
  if (projects && !project) return null;
  const name = project?.projectName?.trim() || projectId;
  const segments = rest.split('/').filter(Boolean);
  const sectionSegment = DETAIL_SECTIONS.has(segments[0]) ? segments[0] : segments.at(-1);
  const section = sectionSegment ? humanize(sectionSegment) : null;
  return {
    to: path,
    title: section ? `${name} › ${section}` : name,
    kind: section ? detailKind(project) : 'Overview',
  };
}
