import { useState, useRef, useEffect } from 'react';
import { ChevronDown, Database } from 'lucide-react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useInstances } from '../../hooks/useProvisioning';
import { useRouteProjectId } from '../../hooks/useRouteProjectId';
import { cn } from '../../utils/cn';

// The same page of another project: picking one navigates there, it never
// re-targets the page that is open.
export function projectPath(pathname: string, from: string, to: string): string {
  const prefix = `/project/${from}`;
  const rest = pathname.startsWith(prefix) ? pathname.slice(prefix.length) : '';
  return `/project/${to}${rest}`;
}

export function ProjectSwitcher() {
  const projectId = useRouteProjectId();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const { data: instances = [] } = useInstances();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);

  return (
    <div ref={ref} className="relative">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-2 px-3 py-1.5 rounded-lg border border-border-primary bg-bg-secondary hover:bg-surface-hover transition-colors text-sm"
        data-testid="project-switcher"
      >
        <Database className="w-4 h-4 text-purple-400" />
        <span className="text-text-primary font-medium max-w-[160px] truncate">
          {projectId || 'Select project'}
        </span>
        <ChevronDown className={cn('w-3.5 h-3.5 text-text-tertiary transition-transform', open && 'rotate-180')} />
      </button>

      {open && (
        <div className="absolute top-full left-0 mt-1 w-56 bg-surface-card border border-border-primary rounded-xl shadow-lg z-50 py-1 max-h-64 overflow-y-auto">
          {instances.length === 0 ? (
            <div className="px-3 py-4 text-center text-xs text-text-tertiary">No projects</div>
          ) : (
            instances.map((inst) => (
              <button
                key={inst.projectId}
                onClick={() => { setOpen(false); navigate(projectPath(pathname, projectId, inst.projectId)); }}
                className={cn(
                  'w-full text-left px-3 py-2 text-sm transition-colors flex items-center gap-2',
                  inst.projectId === projectId
                    ? 'bg-purple-500/10 text-purple-400'
                    : 'text-text-secondary hover:bg-surface-hover hover:text-text-primary'
                )}
              >
                <Database className="w-3.5 h-3.5 flex-shrink-0" />
                <span className="truncate">{inst.projectId}</span>
                <span className="ml-auto text-[10px] text-text-tertiary uppercase">{inst.tier}</span>
              </button>
            ))
          )}
        </div>
      )}
    </div>
  );
}
