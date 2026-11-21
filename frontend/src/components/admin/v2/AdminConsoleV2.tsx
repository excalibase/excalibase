import type { ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { NeedsAttention } from '../NeedsAttention';
import { AdminTabs, type AdminTab } from './AdminTabs';
import { OrgsAdminTable } from './OrgsAdminTable';
import { ProjectsAdminTable } from './ProjectsAdminTable';
import { UsersAdminTable } from './UsersAdminTable';

interface AdminConsoleV2Props {
  readonly canMutate: boolean;
  // Capacity, storage, plans and sign-in providers: the cards that are not lists.
  readonly platform: ReactNode;
}

// The tabs an admin sees. Only a platform admin reads the console's own
// lists (people's addresses), so an operator sees projects and the platform cards.
export function adminTabs(canMutate: boolean): AdminTab[] {
  return [
    ...(canMutate ? [{ id: 'attention', label: 'Needs attention' }] : []),
    { id: 'projects', label: 'Projects' },
    ...(canMutate ? [{ id: 'users', label: 'Users' }, { id: 'orgs', label: 'Organizations' }] : []),
    { id: 'platform', label: 'Platform' },
  ];
}

// An opened project (?project=) lives on the Projects tab, so a link to it lands there.
export function initialTab(tabs: readonly AdminTab[], requested: string | null, project: string | null): string {
  if (requested && tabs.some((tab) => tab.id === requested)) return requested;
  if (project) return 'projects';
  return tabs[0].id;
}

// The admin console behind studio_v2: one list per tab, each paged on the server.
export function AdminConsoleV2({ canMutate, platform }: AdminConsoleV2Props) {
  const [params, setParams] = useSearchParams();
  const tabs = adminTabs(canMutate);
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
      <div role="tabpanel" id={`admin-panel-${active}`} aria-labelledby={`admin-tab-${active}`} className="space-y-6">
        {active === 'attention' && <NeedsAttention />}
        {active === 'projects' && <ProjectsAdminTable canMutate={canMutate} />}
        {active === 'users' && <UsersAdminTable />}
        {active === 'orgs' && <OrgsAdminTable canMutate={canMutate} />}
        {active === 'platform' && platform}
      </div>
    </div>
  );
}
