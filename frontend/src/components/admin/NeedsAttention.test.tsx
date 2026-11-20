import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { NeedsAttention } from './NeedsAttention';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn() } }));

const brief = (over: object) => ({ projectId: 'p1', projectName: 'shop', orgId: 'o1', ownerEmail: 'o@x.test', ownerStatus: 'active', status: 'ACTIVE', ...over });

function renderCard(data: object) {
  vi.mocked(api.get).mockResolvedValue({ data } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <NeedsAttention />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const empty = { newlySuspended: [], signups24h: 0, signupDomains: [], failedProjects: [], topCpu: [], topMemory: [], topEgress: [] };

describe('NeedsAttention', () => {
  beforeEach(() => vi.clearAllMocks());

  test('puts sign-up domains, suspended accounts and failed projects first', async () => {
    renderCard({
      ...empty,
      signups24h: 31,
      signupDomains: [{ domain: 'mailinator.test', count: 24 }, { domain: 'gmail.com', count: 7 }],
      newlySuspended: [{ userId: 'u1', email: 'bad@mailinator.test', reason: 'proxy abuse', suspendedAt: '2026-11-16T01:00:00Z' }],
      failedProjects: [brief({ projectId: 'p9', projectName: 'broken', status: 'FAILED', failureReason: 'no capacity' })],
    });

    expect(await screen.findByText('31 sign-ups in the last 24 h')).toBeInTheDocument();
    expect(screen.getByText('mailinator.test')).toBeInTheDocument();
    expect(screen.getByText('bad@mailinator.test')).toBeInTheDocument();
    expect(screen.getByText('proxy abuse')).toBeInTheDocument();
    const failed = screen.getByText('broken').closest('li') as HTMLElement;
    expect(within(failed).getByText(/no capacity/)).toBeInTheDocument();
  });

  test('top projects name their owner and show a suspended owner as suspended', async () => {
    renderCard({ ...empty, topCpu: [brief({ cpuCores: 1.5, ownerStatus: 'suspended' })] });
    const item = (await screen.findByText('shop')).closest('li') as HTMLElement;
    expect(within(item).getByText('o@x.test')).toBeInTheDocument();
    expect(within(item).getByText('Suspended')).toBeInTheDocument();
    expect(within(item).getByText('1.50 cores')).toBeInTheDocument();
  });

  test('says so when nothing needs attention', async () => {
    renderCard(empty);
    expect(await screen.findByText('Nothing needs attention.')).toBeInTheDocument();
  });

  test('egress is explained as single-host only when none is recorded', async () => {
    renderCard(empty);
    expect(await screen.findByText(/Egress is recorded on single-host installs only/)).toBeInTheDocument();
  });
});
