import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ProjectDetailPanel } from './ProjectDetailPanel';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn() } }));

const detail = {
  projectId: 'p1', projectName: 'shop', orgId: 'o1', orgName: 'Acme', orgSlug: 'acme', tier: 'FREE',
  ownerId: 'u1', ownerEmail: 'owner@acme.test', ownerStatus: 'suspended', ownerSuspendedReason: 'proxy abuse',
  databaseType: 'POSTGRESQL', databaseVersion: '16', databaseStatus: 'ACTIVE', noDatabase: false, cpuCores: 0.4, memoryMb: 310,
  apps: [
    { id: 'a1', name: 'web', image: 'nginx:1.27', replicas: 2, status: 'ACTIVE', runningReplicas: 0, cpuMilliPerReplica: 500, memoryMibPerReplica: 512, tier: 'FREE', internal: false },
    { id: 'a2', name: 'cache', image: 'redis:7', replicas: 1, status: 'ACTIVE', runningReplicas: null, cpuMilliPerReplica: 250, memoryMibPerReplica: 256, tier: 'FREE', internal: true },
  ],
  sites: [{ id: 's1', name: 'landing', hostname: 'acme.sites.test' }],
  functions: [{ id: 'hello', active: true }],
  audit: [{ action: 'admin.project.open', userId: 'boss', details: 'checking abuse report', timestamp: '2026-11-16T01:00:00Z' }],
};

function renderPanel(onClose = () => {}) {
  vi.mocked(api.get).mockResolvedValue({ data: detail } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ProjectDetailPanel projectId="p1" onClose={onClose} />
    </QueryClientProvider>,
  );
}

describe('ProjectDetailPanel', () => {
  beforeEach(() => vi.clearAllMocks());

  test('nothing is read until a reason is written', () => {
    renderPanel();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalled();
  });

  test('opening sends the reason, then answers who owns it, what it runs and whether it is up', async () => {
    renderPanel();
    await userEvent.type(screen.getByLabelText('Reason'), 'checking the abuse report');
    await userEvent.click(screen.getByRole('button', { name: 'Open project' }));

    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/admin/console/projects/p1', { params: { reason: 'checking the abuse report' } }));
    expect(await screen.findByText('owner@acme.test')).toBeInTheDocument();
    // The owner's status and the database's: a suspended owner's project is not "Active".
    expect(screen.getAllByText('Suspended')).toHaveLength(2);
    expect(screen.getByText('proxy abuse')).toBeInTheDocument();

    const web = screen.getByText('web').closest('tr') as HTMLElement;
    expect(within(web).getByText('nginx:1.27')).toBeInTheDocument();
    expect(within(web).getByText('0 / 2 running')).toBeInTheDocument();
    expect(within(web).getByText('500m · 512 MiB')).toBeInTheDocument();

    const cache = screen.getByText('cache').closest('tr') as HTMLElement;
    expect(within(cache).getByText('unknown / 1')).toBeInTheDocument();

    expect(screen.getByText('landing')).toBeInTheDocument();
    expect(screen.getByText('hello')).toBeInTheDocument();
    expect(screen.getByText('admin.project.open')).toBeInTheDocument();
  });

  test('cancelling the reason closes the panel without reading anything', async () => {
    const onClose = vi.fn();
    renderPanel(onClose);
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).toHaveBeenCalled();
    expect(api.get).not.toHaveBeenCalled();
  });
});
