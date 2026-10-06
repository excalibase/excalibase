import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { InstancesPage } from './InstancesPage';
import { DashboardPage } from './DashboardPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

// The list reads the project's status, not only its pipeline stage: a
// project deleted from Settings keeps currentStage COMPLETED (or the stage it
// stopped at) while its status says PENDING_DELETION.
const projects = [
  { projectId: 'proj-gone', projectName: 'gone', tier: 'FREE', namespace: 'ns-1', databaseType: 'POSTGRESQL',
    status: 'PENDING_DELETION', currentStage: 'VALIDATING', deletionDueAt: '2026-10-12T10:00:00Z', createdAt: '2026-10-01T00:00:00Z' },
  { projectId: 'proj-live', projectName: 'live', tier: 'FREE', namespace: 'ns-2', databaseType: 'POSTGRESQL',
    status: 'ACTIVE', currentStage: 'COMPLETED', createdAt: '2026-10-01T00:00:00Z' },
];

function renderWith(element: React.ReactElement) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/provision') return { data: projects } as never;
    return { data: [] } as never;
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{element}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('project lists show the project status', () => {
  beforeEach(() => vi.clearAllMocks());

  test.each([
    ['projects page', <InstancesPage key="i" />],
    ['dashboard', <DashboardPage key="d" />],
  ])('%s: a project scheduled for deletion never reads Provisioning', async (_name, page) => {
    renderWith(page);
    const row = (await screen.findByText('proj-gone')).closest('tr') as HTMLElement;
    expect(within(row).getByText(/Scheduled for deletion/)).toHaveTextContent('2026');
    expect(within(row).queryByText(/Provisioning/)).toBeNull();
    const live = screen.getByText('proj-live').closest('tr') as HTMLElement;
    expect(within(live).getByText(/Active/)).toBeInTheDocument();
  });

  test.each([
    ['projects page', <InstancesPage key="i" />],
    ['dashboard', <DashboardPage key="d" />],
  ])('%s: the project column leads with the project name, the id second', async (_name, page) => {
    renderWith(page);
    const name = await screen.findByText('gone');
    const row = name.closest('tr') as HTMLElement;
    expect(within(row).getByText('proj-gone')).toBeInTheDocument();
    expect(name.compareDocumentPosition(within(row).getByText('proj-gone')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  // EXC-555: the summary cards counted by pipeline stage, so a project
  // scheduled for deletion (stage short of COMPLETED) counted as Provisioning.
  test('dashboard cards count by status: a project scheduled for deletion is not provisioning', async () => {
    projects.push(
      { projectId: 'proj-new', projectName: 'new', tier: 'FREE', namespace: 'ns-4', databaseType: 'POSTGRESQL',
        status: 'PROVISIONING', currentStage: 'WAITING_FOR_READY', createdAt: '2026-10-01T00:00:00Z' } as never,
      { projectId: 'proj-broke', projectName: 'broke', tier: 'FREE', namespace: 'ns-5', databaseType: 'POSTGRESQL',
        status: 'FAILED', currentStage: 'CRD_DEPLOYMENT', createdAt: '2026-10-01T00:00:00Z' } as never,
    );
    renderWith(<DashboardPage />);
    await screen.findByText('proj-gone');
    const card = (label: string) => screen.getByText(label, { selector: 'p' }).nextElementSibling;
    expect(card('Total Instances')).toHaveTextContent('4');
    expect(card('Active')).toHaveTextContent('1');
    expect(card('Provisioning')).toHaveTextContent('1');
    expect(card('Failed')).toHaveTextContent('1');
    projects.splice(-2, 2);
  });

  test('a project without a name falls back to its id', async () => {
    projects.push({ projectId: 'proj-noname', tier: 'FREE', namespace: 'ns-3', databaseType: 'POSTGRESQL',
      status: 'ACTIVE', currentStage: 'COMPLETED', createdAt: '2026-10-01T00:00:00Z' } as never);
    renderWith(<DashboardPage />);
    expect(await screen.findAllByText('proj-noname')).toHaveLength(1);
    projects.pop();
  });
});
