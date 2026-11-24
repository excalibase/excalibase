import { Link, useLocation, useNavigate } from 'react-router-dom';
import { ChevronDown, Database } from 'lucide-react';
import { cn } from '../../../utils/cn';
import { useCurrentOrg, useMyProjects } from '../../../hooks/useMemberOrgs';
import { useRouteProjectId } from '../../../hooks/useRouteProjectId';
import { projectPath } from '../ProjectSwitcher';
import { usePopup } from './TopBar';

// Org / Project ▾: the project list holds only projects of the person's own orgs.
export function ProjectCrumb() {
  const projectId = useRouteProjectId();
  const { org } = useCurrentOrg();
  const { data: projects } = useMyProjects();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const { open, setOpen, ref } = usePopup();
  const current = projects.find((p) => p.projectId === projectId);
  const name = current?.projectName?.trim() || projectId;
  return (
    <nav aria-label="Breadcrumb" className="flex items-center gap-2 text-sm text-text-tertiary">
      <Link to="/instances" className="hover:text-text-primary">{org?.name ?? 'Organization'}</Link>
      <span aria-hidden="true">/</span>
      <div ref={ref} className="relative">
        <button type="button" aria-haspopup="listbox" aria-expanded={open} aria-label={`Project: ${name}`} onClick={() => setOpen(!open)}
          className="inline-flex items-center gap-1.5 rounded-lg border border-border-primary bg-bg-secondary px-3 min-h-9 text-text-primary">
          <Database className="w-4 h-4 text-purple-400" aria-hidden="true" /> {name} <ChevronDown className="w-3.5 h-3.5" aria-hidden="true" />
        </button>
        {open && (
          <ul role="listbox" aria-label="Projects" className="absolute left-0 top-full z-30 mt-1 w-64 max-h-72 overflow-y-auto rounded-xl border border-border-primary bg-surface-card p-1 shadow-lg">
            {projects.map((p) => (
              <li key={p.projectId} role="option" aria-selected={p.projectId === projectId} tabIndex={0}
                onClick={() => { setOpen(false); navigate(projectPath(pathname, projectId, p.projectId)); }}
                onKeyDown={(e) => { if (e.key === 'Enter') { setOpen(false); navigate(projectPath(pathname, projectId, p.projectId)); } }}
                className="cursor-pointer truncate rounded-lg px-3 py-2 text-sm text-text-secondary hover:bg-surface-hover hover:text-text-primary">
                {p.projectName?.trim() || p.projectId}
              </li>
            ))}
          </ul>
        )}
      </div>
    </nav>
  );
}

// Backups and the other pages stay in the rail.
const TABS = [
  { label: 'Services', to: '' },
  { label: 'Variables', to: 'variables' },
  { label: 'Metrics', to: 'monitoring/metrics' },
  { label: 'Logs', to: 'monitoring/logs' },
  { label: 'Settings', to: 'settings' },
];

// Where a page of the project sits among the tabs; pages with no tab keep none selected.
export function activeTab(pathname: string, projectId: string): string | undefined {
  const rest = pathname.replace(`/project/${projectId}`, '').replace(/^\//, '');
  return TABS.find((t) => (t.to === '' ? rest === '' : rest === t.to || rest.startsWith(`${t.to}/`)))?.to;
}

function tabPath(projectId: string, to: string): string {
  return to ? `/project/${projectId}/${to}` : `/project/${projectId}`;
}

export function ProjectTabs() {
  const projectId = useRouteProjectId();
  const { pathname } = useLocation();
  const active = activeTab(pathname, projectId);
  return (
    <nav aria-label="Project" className="flex flex-wrap gap-1 border-b border-border-primary bg-surface-card px-4">
      {TABS.map((tab) => (
        <Link key={tab.label} to={tabPath(projectId, tab.to)} aria-current={active === tab.to ? 'page' : undefined}
          className={cn('border-b-2 px-3 py-3 text-sm', active === tab.to ? 'border-purple-500 text-text-primary' : 'border-transparent text-text-secondary hover:text-text-primary')}>
          {tab.label}
        </Link>
      ))}
    </nav>
  );
}
