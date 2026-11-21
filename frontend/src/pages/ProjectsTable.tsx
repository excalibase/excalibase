import { Link } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { DataTable, type Column, type FilterDef } from '../components/ui/DataTable';
import { StatusBadge } from '../components/shared/StatusBadge';
import { ProjectNameCell } from '../components/shared/ProjectNameCell';
import { useCurrentOrg, useMyProjects, planLabel } from '../hooks/useMemberOrgs';
import { engineIcon, engineLabel } from '../utils/engine';
import type { DatabaseInstance } from '../types';

const COLUMNS: Column<DatabaseInstance>[] = [
  {
    id: 'project',
    header: 'Project',
    locked: true,
    text: (p) => `${p.projectName ?? ''} ${p.projectId}`.trim(),
    sortValue: (p) => p.projectName?.trim() || p.projectId,
    cell: (p) => (
      <Link to={`/project/${p.projectId}`} className="flex items-center gap-2 hover:underline">
        <span aria-hidden="true">{engineIcon(p)}</span>
        <ProjectNameCell projectId={p.projectId} projectName={p.projectName} />
      </Link>
    ),
  },
  { id: 'type', header: 'Type', text: engineLabel, sortValue: engineLabel, cell: (p) => engineLabel(p) },
  { id: 'plan', header: 'Plan', sortValue: (p) => p.tier, cell: (p) => planLabel(p.tier) },
  {
    id: 'status',
    header: 'Status',
    sortValue: (p) => p.status,
    cell: (p) => <StatusBadge status={p.status} stage={p.currentStage} deletionDueAt={p.deletionDueAt} />,
  },
  { id: 'created', header: 'Created', sortValue: (p) => Date.parse(p.createdAt), cell: (p) => new Date(p.createdAt).toLocaleDateString() },
];

function optionsOf(projects: readonly DatabaseInstance[], pick: (p: DatabaseInstance) => string, label: (value: string) => string) {
  return [...new Set(projects.map(pick))].sort((a, b) => a.localeCompare(b)).map((value) => ({ value, label: label(value) }));
}

function filtersFor(projects: readonly DatabaseInstance[]): FilterDef<DatabaseInstance>[] {
  return [
    { id: 'status', label: 'Status', options: optionsOf(projects, (p) => p.status, (v) => v.charAt(0) + v.slice(1).toLowerCase().replaceAll('_', ' ')), matches: (p, v) => p.status === v },
    { id: 'plan', label: 'Plan', options: optionsOf(projects, (p) => p.tier, planLabel), matches: (p, v) => p.tier === v },
    { id: 'type', label: 'Type', options: optionsOf(projects, engineLabel, (v) => v), matches: (p, v) => engineLabel(p) === v },
  ];
}

// The Projects page of the console layout (studio_v2): the projects of the org in the top bar.
export function ProjectsTable() {
  const { org } = useCurrentOrg();
  const { data, isLoading, error, refetch } = useMyProjects();
  const projects = org ? data.filter((p) => p.orgId === org.id) : data;
  return (
    <div className="mx-auto max-w-7xl space-y-4" data-testid="projects-table">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold text-text-primary">Projects</h1>
        <Link to="/provision" className="inline-flex items-center gap-1.5 rounded-lg bg-purple-600 px-3 min-h-9 text-sm font-medium text-white hover:bg-purple-500">
          <Plus className="w-4 h-4" aria-hidden="true" /> New project
        </Link>
      </div>
      <DataTable
        label="Projects"
        columns={COLUMNS}
        rows={projects}
        getRowId={(p) => p.projectId}
        isLoading={isLoading}
        error={error}
        onRetry={refetch}
        filters={filtersFor(projects)}
        storageKey="projects"
        emptyState={
          <div>
            <p>No projects yet.</p>
            <Link to="/provision" className="mt-2 inline-block text-purple-400 hover:underline">Create your first project</Link>
          </div>
        }
      />
    </div>
  );
}
