import type { ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useFeatureEnabled } from '../../../hooks/useDeploymentMode';
import { useAbuseAnalytics, useUserAnalytics } from '../../../hooks/useAdminAnalytics';
import { NeedsAttention } from '../NeedsAttention';
import { AbuseAnalyticsTab } from '../analytics/AbuseAnalyticsTab';
import { AttentionChips } from '../analytics/AttentionChips';
import { ANALYTICS_TABS, isAnalyticsTab } from '../analytics/analyticsTabs';
import { StorageAnalyticsTab } from '../analytics/StorageAnalyticsTab';
import { TrafficAnalyticsTab } from '../analytics/TrafficAnalyticsTab';
import { UsersAnalyticsTab } from '../analytics/UsersAnalyticsTab';
import { AdminTabs, type AdminTab } from './AdminTabs';
import { OrgsAdminTable } from './OrgsAdminTable';
import { ProjectsAdminTable } from './ProjectsAdminTable';
import { UsersAdminTable } from './UsersAdminTable';

interface AdminConsoleV2Props {
  readonly canMutate: boolean;
  // Capacity, storage, plans and sign-in providers: the cards that are not lists.
  readonly platform: ReactNode;
}

// What the analytics tabs show beside their names; empty until the numbers arrive.
export interface AnalyticsBadges {
  signups?: number;
  stillActive?: number;
}

function countBadge(count: number | undefined, alarming: boolean): AdminTab['badge'] {
  if (count === undefined) return undefined;
  return { text: String(count), tone: alarming && count > 0 ? 'bad' : 'neutral' };
}

// The tabs an admin sees. Only a platform admin reads the console's own
// lists (people's addresses), so an operator sees projects and the platform
// cards. The analytics tabs sit between the lists and the platform cards and
// exist only for a platform admin while FEATURE_ADMINANALYTICS is on.
export function adminTabs(canMutate: boolean, analytics = false, badges: AnalyticsBadges = {}): AdminTab[] {
  const analyticsTabs: AdminTab[] = canMutate && analytics
    ? [
      { id: ANALYTICS_TABS.users, label: 'Sign-ups', badge: countBadge(badges.signups, false) },
      { id: ANALYTICS_TABS.abuse, label: 'Abuse', badge: countBadge(badges.stillActive, true) },
      { id: ANALYTICS_TABS.traffic, label: 'Traffic', badge: { text: 'no data', tone: 'warn' } },
      { id: ANALYTICS_TABS.storage, label: 'Storage', badge: { text: 'no data', tone: 'warn' } },
    ]
    : [];
  return [
    ...(canMutate ? [{ id: 'attention', label: 'Needs attention' }] : []),
    { id: 'projects', label: 'Projects' },
    ...(canMutate ? [{ id: 'users', label: 'Users' }, { id: 'orgs', label: 'Organizations' }] : []),
    ...analyticsTabs,
    { id: 'platform', label: 'Platform' },
  ];
}

// An opened project (?project=) lives on the Projects tab, so a link to it lands there.
export function initialTab(tabs: readonly AdminTab[], requested: string | null, project: string | null): string {
  if (requested && tabs.some((tab) => tab.id === requested)) return requested;
  if (project) return 'projects';
  return tabs[0].id;
}

// The admin console: one section per tab, each list paged on the server.
export function AdminConsoleV2({ canMutate, platform }: AdminConsoleV2Props) {
  const [params, setParams] = useSearchParams();
  const { enabled: flagOn } = useFeatureEnabled('adminanalytics');
  const analytics = canMutate && flagOn;
  const signups = useUserAnalytics(analytics).data?.signups;
  const stillActive = useAbuseAnalytics(analytics).data?.refusedStillActive;
  const tabs = adminTabs(canMutate, analytics, { signups, stillActive });
  const active = initialTab(tabs, params.get('tab'), params.get('project'));
  const select = (id: string) => {
    const next = new URLSearchParams(params);
    next.set('tab', id);
    if (id !== 'projects') next.delete('project');
    setParams(next, { replace: true });
  };
  return (
    <div className="mx-auto max-w-7xl space-y-4" data-testid="admin-console-v2">
      <div>
        <h1 className="text-xl font-semibold text-text-primary">Admin console</h1>
        <p className="text-sm text-text-secondary">Every action is logged with a reason.</p>
      </div>
      <AdminTabs tabs={tabs} active={active} onChange={select} />
      {isAnalyticsTab(active) && <AttentionChips onGo={select} />}
      <div role="tabpanel" id={`admin-panel-${active}`} aria-labelledby={`admin-tab-${active}`} className="space-y-6">
        {active === 'attention' && <NeedsAttention />}
        {active === 'projects' && <ProjectsAdminTable canMutate={canMutate} />}
        {active === 'users' && <UsersAdminTable />}
        {active === 'orgs' && <OrgsAdminTable canMutate={canMutate} />}
        {active === ANALYTICS_TABS.users && <UsersAnalyticsTab />}
        {active === ANALYTICS_TABS.abuse && <AbuseAnalyticsTab />}
        {active === ANALYTICS_TABS.traffic && <TrafficAnalyticsTab />}
        {active === ANALYTICS_TABS.storage && <StorageAnalyticsTab />}
        {active === 'platform' && platform}
      </div>
    </div>
  );
}
