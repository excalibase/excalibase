import { useState } from 'react';
import { Outlet, useLocation } from 'react-router-dom';
import { InstanceProvider } from '../../context/InstanceContext';
import { IconRail } from './IconRail';
import { SubNav } from './SubNav';
import { ProjectHeader } from './ProjectHeader';
import { CommandMenu } from '../ui/CommandMenu';
import { PROJECT_NAV } from './navigation';

function ProjectLayoutInner() {
  const location = useLocation();

  const initialSection = deriveSection(location.pathname);
  const [activeSection, setActiveSection] = useState(initialSection);

  const currentSection = deriveSection(location.pathname);
  const showSubNav = currentSection && PROJECT_NAV.find((s) => s.key === currentSection)?.children;

  return (
    <div className="flex h-screen bg-bg-primary overflow-hidden">
      <CommandMenu />
      <IconRail
        activeSection={showSubNav ? currentSection : activeSection}
        onSectionClick={setActiveSection}
      />
      {showSubNav && currentSection && <SubNav sectionKey={currentSection} />}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        <ProjectHeader />
        <main className="flex-1 overflow-y-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function deriveSection(pathname: string): string | null {
  for (const section of PROJECT_NAV) {
    if (section.children) {
      if (section.children.some((c) => pathname.includes(`/${c.to.split('/')[0]}/`) || pathname.endsWith(`/${c.to}`))) {
        return section.key;
      }
    }
  }
  return null;
}

export function ProjectLayout() {
  return (
    <InstanceProvider>
      <ProjectLayoutInner />
    </InstanceProvider>
  );
}
