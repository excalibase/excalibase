import { Home, Database, Gauge, KeyRound, Users, Settings, BookOpen, ShieldCheck, type LucideIcon } from 'lucide-react';

export interface ShellNavItem {
  key: string;
  label: string;
  icon: LucideIcon;
  to: string;
  external?: boolean;
}

export const DOCS_URL = 'https://excalibase.io/docs';

// The left navigation, in order. Status joins it once there is a status page to open. Org pages open the org the person is working in.
export function shellNav(orgId: string | undefined): { main: ShellNavItem[]; help: ShellNavItem[] } {
  const orgPage = (tab: string) => (orgId ? `/orgs/${orgId}?tab=${tab}` : '/orgs');
  const main: ShellNavItem[] = [
    { key: 'home', label: 'Home', icon: Home, to: '/' },
    { key: 'projects', label: 'Projects', icon: Database, to: '/instances' },
    { key: 'usage', label: 'Usage & credits', icon: Gauge, to: '/usage' },
    { key: 'tokens', label: 'Access tokens', icon: KeyRound, to: '/account/tokens' },
    { key: 'members', label: 'Members & roles', icon: Users, to: orgPage('members') },
    { key: 'org-settings', label: 'Organization settings', icon: Settings, to: orgPage('settings') },
  ];
  const help: ShellNavItem[] = [{ key: 'docs', label: 'Docs', icon: BookOpen, to: DOCS_URL, external: true }];
  return { main, help };
}

export const ADMIN_CONSOLE: ShellNavItem = { key: 'admin', label: 'Admin console', icon: ShieldCheck, to: '/admin' };

export const PLATFORM_ADMIN_ROLES = new Set(['platform_admin', 'platform_operator']);
