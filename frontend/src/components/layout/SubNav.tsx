import { NavLink, useParams } from 'react-router-dom';
import { PROJECT_NAV, visibleNavItems } from './navigation';
import { useProjectIsDocumentDB } from '../../hooks/useDocuments';
import { cn } from '../../utils/cn';

interface SubNavProps {
  readonly sectionKey: string;
}

export function SubNav({ sectionKey }: SubNavProps) {
  const { projectId } = useParams<{ projectId: string }>();
  const section = PROJECT_NAV.find((s) => s.key === sectionKey);
  const { data: documentDb = false } = useProjectIsDocumentDB(projectId ?? '');

  if (!section?.children) return null;

  return (
    <div className="w-56 flex-shrink-0 flex flex-col bg-surface-card border-r border-border-primary" data-testid="sub-nav">
      <div className="px-4 py-3 border-b border-border-primary">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-text-tertiary">
          {section.label}
        </h2>
      </div>
      <nav className="flex-1 py-2 px-2 space-y-0.5 overflow-y-auto">
        {visibleNavItems(section.children, documentDb).map((item) => {
          const Icon = item.icon;
          return (
            <NavLink
              key={item.to}
              to={`/project/${projectId}/${item.to}`}
              className={({ isActive }) =>
                cn(
                  'flex items-center gap-2.5 px-3 py-2 rounded-md text-[13px] font-medium transition-colors',
                  isActive
                    ? 'bg-purple-500/10 text-purple-400 border-l-2 border-purple-400 -ml-[2px] pl-[14px]'
                    : 'text-text-secondary hover:text-text-primary hover:bg-surface-hover'
                )
              }
            >
              <Icon className="w-4 h-4 flex-shrink-0" />
              {item.label}
            </NavLink>
          );
        })}
      </nav>
    </div>
  );
}
