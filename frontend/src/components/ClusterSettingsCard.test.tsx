import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ClusterSettingsCard } from './ClusterSettingsCard';
import { api } from '../api/client';
import type { DatabaseInstance } from '../types';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }));

const settings = {
  projectId: 'p-1',
  tier: 'FREE',
  orgTier: 'FREE',
  storageSize: '2Gi',
  storageLimit: '5Gi',
  instances: 1,
  cpu: '0.5',
  memory: '512Mi',
  parameters: { work_mem: '4MB' },
  tunableParameters: ['jit', 'work_mem'],
};

function project(overrides: Partial<DatabaseInstance> = {}): DatabaseInstance {
  return { projectId: 'p-1', status: 'ACTIVE', ...overrides } as DatabaseInstance;
}

function renderCard(instance: DatabaseInstance, current = settings) {
  vi.mocked(api.get).mockResolvedValue({ data: current } as never);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <ClusterSettingsCard project={instance} />
    </QueryClientProvider>,
  );
}

describe('ClusterSettingsCard', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows the disk against the plan and the size the cluster runs', async () => {
    renderCard(project());
    const card = await screen.findByTestId('cluster-settings-card');
    await waitFor(() => expect(card).toHaveTextContent('2Gi of 5Gi'));
    expect(card).toHaveTextContent('1 instance');
    expect(card).toHaveTextContent('512Mi');
  });

  test('grows the disk only after confirmation, then shows the answered size', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValueOnce({
      data: { ...settings, storageSize: '4Gi' },
    } as never);
    renderCard(project());

    await user.type(await screen.findByTestId('resize-input'), '4');
    await user.click(screen.getByTestId('resize-btn'));
    expect(api.post).not.toHaveBeenCalled();
    await user.click(await screen.findByTestId('modal-confirm'));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision/p-1/storage', { size: '4Gi' }),
    );
    expect(await screen.findByTestId('cluster-settings-card')).toHaveTextContent('4Gi of 5Gi');
  });

  test('never offers a size at or below the current disk', async () => {
    const user = userEvent.setup();
    renderCard(project());
    await user.type(await screen.findByTestId('resize-input'), '2');
    expect(screen.getByTestId('resize-btn')).toBeDisabled();
  });

  test('shows the control plane reason when a change is refused', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockRejectedValueOnce({
      response: { data: { error: 'postgres parameter not allowed: work_mem' } },
    });
    renderCard(project());

    const row = await screen.findByTestId('param-work_mem');
    await user.clear(within(row).getByRole('textbox'));
    await user.type(within(row).getByRole('textbox'), '1GB');
    await user.click(screen.getByTestId('params-save-btn'));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/provision/p-1/parameters', {
        parameters: { work_mem: '1GB' },
      }),
    );
    expect(await screen.findByTestId('cluster-settings-error')).toHaveTextContent(
      'postgres parameter not allowed',
    );
  });

  test('sends only the settings that have a value', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockResolvedValueOnce({
      data: { ...settings, parameters: { jit: 'off' } },
    } as never);
    renderCard(project());

    await user.clear(within(await screen.findByTestId('param-work_mem')).getByRole('textbox'));
    await user.type(within(screen.getByTestId('param-jit')).getByRole('textbox'), 'off');
    await user.click(screen.getByTestId('params-save-btn'));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/provision/p-1/parameters', {
        parameters: { jit: 'off' },
      }),
    );
  });

  // The tier follows the organization; the card can only apply the org's plan.
  test('offers to move onto the organization plan when it differs', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValueOnce({
      data: { ...settings, tier: 'STANDARD', orgTier: 'STANDARD' },
    } as never);
    renderCard(project(), { ...settings, orgTier: 'STANDARD' });

    await user.click(await screen.findByTestId('tier-apply-btn'));
    await user.click(await screen.findByTestId('modal-confirm'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision/p-1/tier', { tier: 'STANDARD' }),
    );
  });

  test('has nothing to apply when the project is on the organization plan', async () => {
    renderCard(project());
    expect(await screen.findByTestId('tier-apply-btn')).toBeDisabled();
  });

  test('is closed on a project that is not active', async () => {
    renderCard(project({ status: 'PAUSED' }));
    expect(await screen.findByTestId('cluster-settings-inactive')).toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalled();
  });
});
