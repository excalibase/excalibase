import { describe, test, expect, vi, beforeEach } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { DatabaseServiceCard } from './DatabaseServiceCard';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }));

const RAW = 'create db: pod secret-pod-7 CrashLoopBackOff on node n7';
const settle = () => act(async () => new Promise((resolve) => setTimeout(resolve, 150)));

describe('DatabaseServiceCard, failed provisioning', () => {
  beforeEach(() => vi.clearAllMocks());

  test('says it in plain words and never renders the raw text', async () => {
    vi.mocked(api.get).mockImplementation((url: string) =>
      url === '/provision/proj-1'
        ? Promise.resolve({
            data: { projectId: 'proj-1', orgId: 'o', databaseType: 'POSTGRESQL', postgresVersion: '17', tier: 'FREE', status: 'FAILED', currentStage: 'FAILED', failureReason: RAW },
          } as never)
        : Promise.resolve({ data: {} } as never),
    );
    const view = render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter><DatabaseServiceCard projectId="proj-1" /></MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByText(/kept crashing/i)).toBeInTheDocument();
    await settle();
    expect(view.container.innerHTML).not.toContain('secret-pod-7');
    expect(view.container.innerHTML).not.toContain('CrashLoopBackOff');
  });

  test('a customer answer with only a failure kind still says it in plain words', async () => {
    vi.mocked(api.get).mockImplementation((url: string) =>
      url === '/provision/proj-1'
        ? Promise.resolve({
            data: { projectId: 'proj-1', orgId: 'o', databaseType: 'POSTGRESQL', postgresVersion: '17', tier: 'FREE', status: 'FAILED', currentStage: 'FAILED', failureKind: 'storage' },
          } as never)
        : Promise.resolve({ data: {} } as never),
    );
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter><DatabaseServiceCard projectId="proj-1" /></MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByText(/storage could not be attached/i)).toBeInTheDocument();
  });

  const renderCard = (data: Record<string, unknown>) => {
    vi.mocked(api.get).mockImplementation((url: string) =>
      url === '/provision/proj-1'
        ? Promise.resolve({ data: { projectId: 'proj-1', orgId: 'o', tier: 'FREE', status: 'ACTIVE', ...data } } as never)
        : Promise.resolve({ data: {} } as never),
    );
    return render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter><DatabaseServiceCard projectId="proj-1" /></MemoryRouter>
      </QueryClientProvider>,
    );
  };

  test('a project without a database says a kind in plain words', async () => {
    renderCard({ noDatabase: true, failureKind: 'image' });
    expect(await screen.findByText(/image could not be downloaded/i)).toBeInTheDocument();
  });

  test('a project without a database shows the server sentence when it sends one', async () => {
    renderCard({ noDatabase: true, failureKind: 'unknown', failureMessage: 'the database add was interrupted; its resources were removed' });
    expect(await screen.findByText('the database add was interrupted; its resources were removed')).toBeInTheDocument();
  });

  test('a project without a database and without a failure shows no failure', async () => {
    renderCard({ noDatabase: true });
    await screen.findByTestId('service-database-empty');
    expect(screen.queryByText(/Provisioning failed|tell us/)).not.toBeInTheDocument();
  });

  test('a failed database shows the server sentence as it is', async () => {
    renderCard({ databaseType: 'POSTGRESQL', postgresVersion: '17', status: 'FAILED', currentStage: 'FAILED', failureKind: 'unknown', failureMessage: 'the restore did not complete; restore again from Backups' });
    expect(await screen.findByText('the restore did not complete; restore again from Backups')).toBeInTheDocument();
  });
});
