import { useState } from 'react';
import { Outlet } from 'react-router-dom';
import { TopBar } from './TopBar';
import { OrgSwitcher } from './OrgSwitcher';
import { SideNav } from './SideNav';
import { CommandPalette } from './CommandPalette';

// The console layout behind the studio_v2 flag: top bar, left navigation, page.
export function StudioShell() {
  const [searching, setSearching] = useState(false);
  return (
    <div data-testid="studio-shell" className="flex h-screen flex-col overflow-hidden bg-bg-primary">
      <TopBar left={<OrgSwitcher />} onSearch={() => setSearching(true)} />
      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <SideNav />
        <main className="min-w-0 flex-1 overflow-y-auto p-4 md:p-6"><Outlet /></main>
      </div>
      <CommandPalette open={searching} onOpenChange={setSearching} />
    </div>
  );
}
