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
});
