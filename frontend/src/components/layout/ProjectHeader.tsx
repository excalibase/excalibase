import { useLocation, useParams } from 'react-router-dom';
import { Sun, Moon, ChevronRight, LogOut } from 'lucide-react';
import { ProjectSwitcher } from './ProjectSwitcher';
import { PROJECT_NAV } from './navigation';
import { useDarkMode } from '../../hooks/useDarkMode';
import { useAuthStore } from '../../stores/auth-store';

export function ProjectHeader() {
  const { dark, toggle } = useDarkMode();
  const location = useLocation();
  const { projectId } = useParams<{ projectId: string }>();
  const { logout } = useAuthStore();

  const breadcrumbs = buildBreadcrumbs(location.pathname, projectId ?? '');

  return (
    <header className="flex items-center gap-3 h-12 px-4 bg-surface-card border-b border-border-primary flex-shrink-0">
      <ProjectSwitcher />

      {breadcrumbs.length > 0 && (
        <div className="flex items-center gap-1.5 text-sm">
          {breadcrumbs.map((crumb, i) => (
            <span key={`crumb-${crumb}`} className="flex items-center gap-1.5">
              <ChevronRight className="w-3.5 h-3.5 text-text-tertiary" />
              <span className={i === breadcrumbs.length - 1 ? 'text-text-primary font-medium' : 'text-text-tertiary'}>
                {crumb}
              </span>
            </span>
          ))}
        </div>
      )}

      <div className="ml-auto flex items-center gap-2">
        <button
          onClick={toggle}
          className="p-2 rounded-lg text-text-secondary hover:text-text-primary hover:bg-surface-hover transition-colors"
        >
          {dark ? <Sun className="w-4 h-4" /> : <Moon className="w-4 h-4" />}
        </button>
        <button
          onClick={() => { void logout(); }}
          className="p-2 rounded-lg text-text-secondary hover:text-red-400 hover:bg-surface-hover transition-colors"
          title="Sign out"
        >
          <LogOut className="w-4 h-4" />
        </button>
      </div>
    </header>
  );
}

function buildBreadcrumbs(pathname: string, projectId: string): string[] {
  const relative = pathname.replace(`/project/${projectId}`, '').replace(/^\//, '');
  if (!relative) return [];

  for (const section of PROJECT_NAV) {
    if (section.children) {
      const match = section.children.find((c) => c.to === relative);
      if (match) return [section.label, match.label];
    } else if (section.to === relative) {
      return [section.label];
    }
  }

  return relative.split('/').map((p) => p.charAt(0).toUpperCase() + p.slice(1));
}
