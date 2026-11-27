import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ClusterSettingsCard } from './ClusterSettingsCard';
import { api } from '../api/client';
import { planBudget } from '../test/homeFixtures';
import type { DatabaseInstance } from '../types';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }));

const settings = {
  projectId: 'p-1', tier: 'FREE', orgTier: 'STANDARD', storageSize: '2Gi', storageStart: '5Gi', storageLimit: '5Gi', storageUsedBytes: 0,
  canChange: true, instances: 1, cpu: '0.5', memory: '512Mi', parameters: {}, tunableParameters: [],
};

const REFUSAL = 'plan budget exceeded: this needs 1.50 vCPU more CPU but only 0.25 vCPU is left (your plan allows 2.00 vCPU)';

function mount(features: Record<string, boolean>) {
  vi.mocked(api.get).mockImplementation(((url: string) => {
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features } });
    if (url.endsWith('/plan-budget')) return Promise.resolve({ data: planBudget() });
    return Promise.resolve({ data: settings });
  }) as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ClusterSettingsCard project={{ projectId: 'p-1', orgId: 'org-1', status: 'ACTIVE' } as DatabaseInstance} />
    </QueryClientProvider>,
  );
}

describe('the resize card and the plan budget', () => {
  beforeEach(() => vi.clearAllMocks());

  test('says what is left in the plan beside the change, and shows the refusal text inline', async () => {
    vi.mocked(api.post).mockRejectedValueOnce({ response: { status: 409, data: { code: 'plan_budget_exceeded', error: REFUSAL, status: 409 } } });
    mount({ planbudget: true });
    expect(await screen.findByText(/Left in your plan: 1.5 vCPU, 3 GiB memory, 15 GiB disk/)).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(await screen.findByTestId('tier-apply-btn'));
    await user.click(await screen.findByTestId('modal-confirm'));
    expect(await screen.findByTestId('cluster-settings-error')).toHaveTextContent(REFUSAL);
  });

  test('shows no budget line while the feature is off', async () => {
    mount({});
    await screen.findByTestId('tier-apply-btn');
    expect(api.get).toHaveBeenCalledWith('/config');
    expect(screen.queryByText(/Left in your plan/)).toBeNull();
  });
});
