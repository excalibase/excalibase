import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { DataTable, type Column, type FilterDef } from '../../components/ui/DataTable';
import { ProjectNameCell } from '../../components/shared/ProjectNameCell';
import { useCurrentOrg, useMyProjects } from '../../hooks/useMemberOrgs';
import { useDocumentTitle } from '../../hooks/useDocumentTitle';
import { cn } from '../../utils/cn';
import { containsLabel, lastUsedAt, projectStatus, relativeTime, rowAction, servicesOf, type ProjectServices, type ProjectStatus, type Tone } from './model';
import { ResumeButton } from './ResumeButton';
import { useProjectServices, type LoadedServices } from './useHomeData';
import type { DatabaseInstance } from '../../types';

interface ProjectRow {
  project: DatabaseInstance;
  services: ProjectServices;
  status: ProjectStatus;
  contains: string;
  lastUsed: number | null;
}

const TONE: Record<Tone, string> = { green: 'text-green-400', amber: 'text-amber-400', red: 'text-red-400', blue: 'text-blue-400', neutral: 'text-text-secondary' };
const ACTION = 'text-purple-400 hover:underline focus-visible:underline';

const nameOf = (row: ProjectRow) => row.project.projectName?.trim() || row.project.projectId;

export function buildRows(projects: readonly DatabaseInstance[], services: (projectId: string) => ProjectServices): ProjectRow[] {
  return projects
    .map((project) => {
      const known = services(project.projectId);
      return { project, services: known, status: projectStatus(project, known), contains: containsLabel(project, known), lastUsed: lastUsedAt(project) };
    })
    .sort((a, b) => (b.lastUsed ?? 0) - (a.lastUsed ?? 0) || Date.parse(b.project.createdAt) - Date.parse(a.project.createdAt));
}

function columns(now: Date): Column<ProjectRow>[] {
  return [
    {
      id: 'project', header: 'Project', locked: true,
      text: (row) => `${nameOf(row)} ${row.project.projectId}`,
      sortValue: nameOf,
      cell: (row) => (
        <Link to={`/project/${row.project.projectId}`} className="hover:underline">
          <ProjectNameCell projectId={row.project.projectId} projectName={row.project.projectName} />
        </Link>
      ),
    },
    { id: 'contains', header: 'Contains', sortValue: (row) => row.contains, cell: (row) => row.contains },
    {
      id: 'status', header: 'Status', sortValue: (row) => row.status.label,
      cell: (row) => <span className={cn('font-medium', TONE[row.status.tone])}>{row.status.label}</span>,
    },
    { id: 'created', header: 'Created', sortValue: (row) => Date.parse(row.project.createdAt), cell: (row) => <span className="text-text-secondary">{new Date(row.project.createdAt).toLocaleDateString()}</span> },
    {
      id: 'used', header: 'Last used', sortValue: (row) => row.lastUsed,
      cell: (row) => <span className="text-text-secondary">{row.lastUsed === null ? '—' : relativeTime(row.lastUsed, now)}</span>,
    },
  ];
}

function RowAction({ row }: { readonly row: ProjectRow }) {
  const action = rowAction(row.project, row.status);
  return action.resumeProjectId ? (
    <ResumeButton projectId={action.resumeProjectId} name={nameOf(row)} className={ACTION} />
  ) : (
    <Link to={action.to ?? '/'} aria-label={`${action.label} ${nameOf(row)}`} className={ACTION}>{action.label}</Link>
  );
}

const STATUS_OPTIONS = [
  { value: 'running', label: 'Running' }, { value: 'paused', label: 'Paused' }, { value: 'empty', label: 'No services yet' },
  { value: 'working', label: 'Changing' }, { value: 'failed', label: 'Failed' }, { value: 'deleting', label: 'Being deleted' },
];

const FILTERS: FilterDef<ProjectRow>[] = [
  { id: 'status', label: 'Status', options: STATUS_OPTIONS, matches: (row, value) => row.status.key === value },
  {
    id: 'contains', label: 'Contains',
    options: [{ value: 'database', label: 'Database' }, { value: 'app', label: 'Apps' }, { value: 'site', label: 'Sites' }, { value: 'none', label: 'Nothing yet' }],
    matches: (row, value) => {
      if (value === 'database') return row.project.noDatabase !== true;
      if (value === 'app') return (row.services.apps ?? 0) > 0;
      if (value === 'site') return (row.services.sites ?? 0) > 0;
      return row.contains === 'Empty project';
    },
  },
];

function failedLists(loaded: LoadedServices | undefined): ('apps' | 'sites')[] {
  return [...(loaded?.appsFailed ? ['apps' as const] : []), ...(loaded?.sitesFailed ? ['sites' as const] : [])];
}

const renderRowAction = (row: ProjectRow) => <RowAction row={row} />;

// The Projects page of the console layout (studio_v2): the projects of the org in the top bar.
export function ProjectsPage() {
  useDocumentTitle('Projects');
  const { org } = useCurrentOrg();
  const { data, isLoading, error, refetch } = useMyProjects();
  const projects = useMemo(() => (org ? data.filter((p) => p.orgId === org.id) : data), [org, data]);
  const { byProject } = useProjectServices(projects);
  const rows = buildRows(projects, (id) => servicesOf(byProject.get(id)?.apps, byProject.get(id)?.sites, failedLists(byProject.get(id))));
  const tableColumns = useMemo(() => columns(new Date()), []);

  return (
    <div className="mx-auto max-w-7xl space-y-4" data-testid="projects-page">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-2xl font-semibold text-text-primary">Projects</h1>
        {!isLoading && !error && (
          <span className="text-sm text-text-secondary">{org ? `${org.name} · ` : ''}{projects.length} project{projects.length === 1 ? '' : 's'}</span>
        )}
        <Link to="/provision" className="ml-auto inline-flex items-center gap-1.5 rounded-lg bg-purple-600 px-3 min-h-9 text-sm font-medium text-white hover:bg-purple-500">
          <Plus className="h-4 w-4" aria-hidden="true" /> New project
        </Link>
      </div>
      <DataTable
        label="Projects"
        columns={tableColumns}
        rows={rows}
        getRowId={(row) => row.project.projectId}
        isLoading={isLoading}
        error={error}
        onRetry={refetch}
        filters={FILTERS}
        rowActions={renderRowAction}
        paging={{ mode: 'client', pageSize: 10 }}
        storageKey="projects-v2"
        emptyState={
          <div>
            <p>No projects yet.</p>
            <Link to="/provision" className="mt-2 inline-block text-purple-400 hover:underline">Create your first project</Link>
          </div>
        }
      />
      <p className="max-w-2xl text-sm text-text-secondary">
        A paused project keeps its data and costs nothing while idle. Resume it from here or from its settings page. Deleting a project removes its services, backups and keys after a 7-day grace period, and asks you to type the project name first.
      </p>
    </div>
  );
}
