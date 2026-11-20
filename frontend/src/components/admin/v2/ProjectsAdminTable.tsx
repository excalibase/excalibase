import { useMemo, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useSearchParams } from 'react-router-dom';
import { Eye, Trash2 } from 'lucide-react';
import { DataTable, type Column, type FilterDef } from '../../ui/DataTable';
import { Button } from '../../Button';
import { StatusBadge } from '../../shared/StatusBadge';
import { serverErrorMessage } from '../../../utils/serverError';
import { api } from '../../../api/client';
import { suspendUser } from '../../../api/adminConsole';
import { useAdminProjectsPage, useForceDropProject, type AdminProject } from '../../../hooks/useAdmin';
import { RESPOND_ASYNC } from '../../../hooks/projectFollow';
import { AccountStatusBadge } from '../AccountStatusBadge';
import { ProjectDetailPanel } from '../ProjectDetailPanel';
import { ReasonDialog } from '../ReasonDialog';
import { BulkDialog, BulkOutcomeNotice, type BulkOutcome, type BulkRequest } from './BulkAction';
import { createdOn, distinctOwners, formatCpu, formatMemory, refreshAdmin } from './adminRows';
import { PROJECT_SORTS } from './listQuery';
import { useServerList } from './useServerList';

const serverSorted = () => '';
const anyRow = () => true;
const planName = (tier: string) => tier.charAt(0).toUpperCase() + tier.slice(1).toLowerCase();

export const PROJECT_FILTERS: FilterDef<AdminProject>[] = [
  {
    id: 'status', label: 'Status', matches: anyRow,
    options: ['ACTIVE', 'PAUSED', 'FAILED', 'PROVISIONING', 'DELETING'].map((value) => ({ value, label: planName(value) })),
  },
  { id: 'plan', label: 'Plan', matches: anyRow, options: ['FREE', 'STANDARD', 'ENTERPRISE'].map((value) => ({ value, label: planName(value) })) },
  { id: 'owner', label: 'Owner', matches: anyRow, options: [{ value: 'active', label: 'Not suspended' }, { value: 'suspended', label: 'Suspended' }] },
  { id: 'flag', label: 'Flags', matches: anyRow, options: [{ value: 'protected', label: 'Deletion protected' }] },
];

export function projectColumns(): Column<AdminProject>[] {
  return [
    {
      id: 'name', header: 'Project', locked: true, sortValue: serverSorted, text: (p) => p.projectName || p.projectId,
      cell: (p) => (<div><div className="font-medium">{p.projectName || p.projectId}</div><div className="font-mono text-xs text-text-tertiary">{p.projectId}</div></div>),
    },
    { id: 'org', header: 'Organization', sortValue: serverSorted, cell: (p) => p.orgName || p.orgId },
    {
      id: 'owner', header: 'Owner', sortValue: serverSorted,
      cell: (p) => (<div><div>{p.ownerEmail ?? '—'}</div>{p.ownerSuspended && <AccountStatusBadge status="suspended" />}</div>),
    },
    { id: 'plan', header: 'Plan', sortValue: serverSorted, cell: (p) => planName(p.tier) },
    { id: 'status', header: 'Status', sortValue: serverSorted, cell: (p) => <StatusBadge status={p.status} /> },
    { id: 'cpu', header: 'CPU', cell: (p) => formatCpu(p.cpuCores) },
    { id: 'memory', header: 'Memory', cell: (p) => formatMemory(p.memBytes) },
    { id: 'flags', header: 'Flags', cell: (p) => (p.deletionProtection ? 'Deletion protected' : '') },
    { id: 'created', header: 'Created', sortValue: serverSorted, cell: (p) => createdOn(p.createdAt) },
  ];
}

// The render props live outside the component so they are not rebuilt as new components on every render.
type SelectionAction = (selected: AdminProject[], clear: () => void) => void;

function projectBulkActions(suspendOwners: SelectionAction, deleteProjects: SelectionAction) {
  return (selected: AdminProject[], clear: () => void) => (
    <>
      <Button size="sm" variant="danger" onClick={() => suspendOwners(selected, clear)}>Suspend owners…</Button>
      <Button size="sm" variant="danger" onClick={() => deleteProjects(selected, clear)}>Delete…</Button>
    </>
  );
}

function projectRowActions(canMutate: boolean, dropping: boolean, on: { open: (p: AdminProject) => void; drop: (p: AdminProject) => void }) {
  return (p: AdminProject) => (
    <span className="inline-flex gap-2">
      {canMutate && <Button size="sm" variant="secondary" onClick={() => on.open(p)}><Eye className="w-3.5 h-3.5 mr-1" aria-hidden="true" />Open</Button>}
      <Button size="sm" variant="danger" disabled={!canMutate || dropping} onClick={() => on.drop(p)}>
        <Trash2 className="w-3.5 h-3.5 mr-1" aria-hidden="true" />Drop
      </Button>
    </span>
  );
}

// Every project on the platform, paged, searched and sorted on the server.
export function ProjectsAdminTable({ canMutate }: { readonly canMutate: boolean }) {
  const list = useServerList(PROJECT_SORTS);
  const { data, isLoading, error, refetch } = useAdminProjectsPage({
    limit: list.params.limit as number,
    offset: list.params.offset as number,
    q: list.params.q as string | undefined,
    sort: list.params.sort as string | undefined,
    order: list.params.order as string | undefined,
    status: list.params.status as string | undefined,
    plan: list.params.plan as string | undefined,
    owner: list.params.owner as string | undefined,
    flag: list.params.flag as string | undefined,
  });
  const client = useQueryClient();
  const drop = useForceDropProject();
  const [params, setParams] = useSearchParams();
  const openProject = params.get('project');
  const [dropping, setDropping] = useState<AdminProject | null>(null);
  const [bulk, setBulk] = useState<BulkRequest<AdminProject> | null>(null);
  const [outcome, setOutcome] = useState<BulkOutcome | null>(null);
  const columns = useMemo(projectColumns, []);

  const showProject = (projectId: string | null) => {
    const next = new URLSearchParams(params);
    if (projectId) next.set('project', projectId);
    else next.delete('project');
    setParams(next, { replace: true });
  };
  const confirmDrop = async (reason: string) => {
    if (!dropping) return;
    try {
      await drop.mutateAsync({ projectId: dropping.projectId, reason });
      setDropping(null);
    } catch {
      // the dialog stays open and shows drop.error
    }
  };
  const settled = (clear: () => void) => () => { clear(); refreshAdmin(client); };

  const offerSuspendOwners = (selected: AdminProject[], clear: () => void) => {
    const owners = distinctOwners(selected);
    if (owners.length === 0) return;
    setBulk({
      title: `Suspend ${owners.length} owner${owners.length === 1 ? '' : 's'}`,
      description: `Blocks the ${owners.length === 1 ? 'account' : 'accounts'} that own the selected projects and scales their apps to zero.`,
      confirmLabel: 'Suspend', danger: true,
      items: owners.map((ownerId) => selected.find((p) => p.ownerId === ownerId)!),
      act: (project, reason) => suspendUser(project.ownerId!, reason),
      label: (project) => project.ownerEmail ?? project.ownerId ?? 'owner', verb: 'Suspended', noun: 'accounts',
      onSettled: settled(clear),
    });
  };
  const offerDrop = (selected: AdminProject[], clear: () => void) => {
    if (selected.length === 0) return;
    setBulk({
      title: `Delete ${selected.length} project${selected.length === 1 ? '' : 's'}`,
      description: 'Bypasses deletion protection and cannot be undone.',
      confirmLabel: 'Delete', danger: true, items: selected,
      act: (project, reason) => api.delete(`/admin/projects/${project.projectId}`, { ...RESPOND_ASYNC, data: { reason } }),
      label: (project) => project.projectName || project.projectId, verb: 'Deleted', noun: 'projects',
      onSettled: settled(clear),
    });
  };

  return (
    <div data-testid="projects-admin-table" className="space-y-3">
      <p className="text-xs text-text-tertiary">Every project on the platform. Delete bypasses deletion protection. Every action asks for a reason that goes to the audit log.</p>
      {outcome && <BulkOutcomeNotice outcome={outcome} onDismiss={() => setOutcome(null)} />}
      <DataTable
        label="Projects"
        columns={columns}
        rows={data?.items ?? []}
        getRowId={(p) => p.projectId}
        isLoading={isLoading}
        error={error}
        onRetry={refetch}
        filters={PROJECT_FILTERS}
        paging={list.paging(data?.total ?? 0)}
        storageKey="admin-projects"
        selectable={canMutate}
        bulkActions={projectBulkActions(offerSuspendOwners, offerDrop)}
        rowActions={projectRowActions(canMutate, drop.isPending, {
          open: (p) => showProject(p.projectId),
          drop: (p) => { drop.reset(); setDropping(p); },
        })}
        emptyState="No projects provisioned."
      />
      {dropping && (
        <ReasonDialog
          title={`Force-drop project "${dropping.projectName || dropping.projectId}"`}
          description="Bypasses deletion protection and cannot be undone."
          confirmLabel="Drop"
          danger
          busy={drop.isPending}
          error={drop.error ? serverErrorMessage(drop.error, 'Drop failed') : undefined}
          onConfirm={confirmDrop}
          onCancel={() => setDropping(null)}
        />
      )}
      {bulk && <BulkDialog request={bulk} onFinished={(done) => { setBulk(null); setOutcome(done); }} onCancel={() => setBulk(null)} />}
      {canMutate && openProject && <ProjectDetailPanel key={openProject} projectId={openProject} onClose={() => showProject(null)} />}
    </div>
  );
}
