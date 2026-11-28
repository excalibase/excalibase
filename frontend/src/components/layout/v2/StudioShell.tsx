import { useState } from 'react';
import { Outlet, useLocation } from 'react-router-dom';
import { TopBar } from './TopBar';
import { OrgSwitcher } from './OrgSwitcher';
import { SideNav } from './SideNav';
import { CommandPalette } from './CommandPalette';
import { NoOrganization } from './NoOrganization';
import { CreditBanner } from './CreditBanner';
import { useMemberOrgs } from '../../../hooks/useMemberOrgs';
import { useRecordVisits } from '../../../pages/home/useRecordVisits';

// The pages that need an organization to show anything; the rest (access keys, the org list, admin) work without one.
const ORG_PAGES = new Set(['/', '/instances']);

// The console layout behind the studio_v2 flag: top bar, left navigation, page.
export function StudioShell() {
  const [searching, setSearching] = useState(false);
  useRecordVisits();
  const { pathname } = useLocation();
  const { orgs, isLoading, error } = useMemberOrgs();
  const noOrg = !isLoading && !error && orgs.length === 0 && ORG_PAGES.has(pathname);
  return (
    <div data-testid="studio-shell" className="flex h-screen flex-col overflow-hidden bg-bg-primary">
      <TopBar left={<OrgSwitcher />} onSearch={() => setSearching(true)} />
      <CreditBanner />
      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <SideNav />
        <main className="min-w-0 flex-1 overflow-y-auto p-4 md:p-6">{noOrg ? <NoOrganization /> : <Outlet />}</main>
      </div>
      <CommandPalette open={searching} onOpenChange={setSearching} />
    </div>
  );
}
