import { useState } from 'react';
import { Outlet, useLocation } from 'react-router-dom';
import { IconRail } from '../IconRail';
import { SubNav } from '../SubNav';
import { deriveSection } from '../ProjectLayout';
import { PROJECT_NAV } from '../navigation';
import { useDocumentTitle } from '../../../hooks/useDocumentTitle';
import { useRouteProjectId } from '../../../hooks/useRouteProjectId';
import { TopBar } from './TopBar';
import { ProjectCrumb, ProjectTabs } from './ProjectBar';
import { CommandPalette } from './CommandPalette';

// A project's pages under the console top bar: Org / Project ▾, then the tabs.
// The deep navigation (icon rail and sub-navigation) stays, so no page loses its menu.
export function ProjectShell() {
  useDocumentTitle(useRouteProjectId());
  const { pathname } = useLocation();
  const [searching, setSearching] = useState(false);
  const [railSection, setRailSection] = useState(deriveSection(pathname));
  const section = deriveSection(pathname);
  const showSubNav = section && PROJECT_NAV.find((s) => s.key === section)?.children;
  return (
    <div data-testid="studio-shell" className="flex h-screen flex-col overflow-hidden bg-bg-primary">
      <TopBar left={<ProjectCrumb />} onSearch={() => setSearching(true)} />
      <ProjectTabs />
      <div className="flex min-h-0 flex-1">
        <IconRail activeSection={showSubNav ? section : railSection} onSectionClick={setRailSection} />
        {showSubNav && section && <SubNav sectionKey={section} />}
        <main className="min-w-0 flex-1 overflow-y-auto p-6"><Outlet /></main>
      </div>
      <CommandPalette open={searching} onOpenChange={setSearching} />
    </div>
  );
}
