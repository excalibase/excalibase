import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MinorUpgradeCard } from './MinorUpgradeCard';
import { api } from '../api/client';
import type { DatabaseInstance } from '../types';

vi.mock('../api/client', () => ({ api: { post: vi.fn() } }));

function project(overrides: Partial<DatabaseInstance> = {}): DatabaseInstance {
  return {
    projectId: 'p-1',
    status: 'ACTIVE',
    postgresVersion: '16',
    ...overrides,
  } as DatabaseInstance;
}

function renderCard(instance: DatabaseInstance) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MinorUpgradeCard project={instance} />
    </QueryClientProvider>
  );
}

async function confirmUpgrade(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('minor-upgrade-btn'));
  await user.click(await screen.findByTestId('modal-confirm'));
}

describe('MinorUpgradeCard', () => {
  beforeEach(() => vi.clearAllMocks());

  test('names the major the project is on', () => {
    renderCard(project({ postgresVersion: '16' }));
    expect(screen.getByTestId('minor-upgrade-card')).toHaveTextContent('PostgreSQL 16');
  });

  // Like every other lifecycle action in Studio, it goes through a
  // confirmation: a rolling restart of a tenant's database is not a click.
  test('does not call the API until the confirmation is accepted', async () => {
    const user = userEvent.setup();
    renderCard(project());

    await user.click(screen.getByTestId('minor-upgrade-btn'));
    expect(api.post).not.toHaveBeenCalled();

    await user.click(await screen.findByTestId('modal-confirm'));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/provision/p-1/upgrade'));
  });

  // The rolling restart is asynchronous. The card shows the state the control
  // plane reported, never a state it hoped for.
  test('reports the status the control plane returned', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValueOnce({
      data: { projectId: 'p-1', status: 'UPGRADING', postgresVersion: '16' },
    } as never);

    renderCard(project());
    await confirmUpgrade(user);

    expect(await screen.findByTestId('minor-upgrade-result')).toHaveTextContent('UPGRADING');
  });

  test('never claims success when the control plane refused', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockRejectedValueOnce(new Error('project is PAUSED'));

    renderCard(project());
    await confirmUpgrade(user);

    expect(await screen.findByTestId('minor-upgrade-error')).toHaveTextContent('project is PAUSED');
    expect(screen.queryByTestId('minor-upgrade-result')).not.toBeInTheDocument();
  });

  test('is closed on a project that is not active', () => {
    renderCard(project({ status: 'PAUSED' }));
    expect(screen.getByTestId('minor-upgrade-btn')).toBeDisabled();
  });

  test('is closed on a project with no recorded major', () => {
    renderCard(project({ postgresVersion: undefined }));
    expect(screen.getByTestId('minor-upgrade-btn')).toBeDisabled();
  });
});
