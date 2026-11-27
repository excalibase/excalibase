import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { AxiosError } from 'axios';
import { ProvisionPage } from './ProvisionPage';
import { api } from '../api/client';
import { listMyOrgs } from '../api/orgs';
import { planBudget } from '../test/homeFixtures';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));
vi.mock('../api/orgs', () => ({ listMyOrgs: vi.fn() }));
vi.mock('react-router-dom', async () => ({ ...(await vi.importActual<typeof import('react-router-dom')>('react-router-dom')), useNavigate: () => vi.fn() }));

const REFUSAL = 'plan budget exceeded: this needs 0.50 vCPU more CPU but only 0.00 vCPU is left (your plan allows 2.00 vCPU)';
const CATALOG = { documentDbRef: 'v1', majors: [{ major: '16', available: true, documentDb: true }] };

function mount(features: Record<string, boolean>) {
  vi.mocked(listMyOrgs).mockResolvedValue([{ id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: 'u1' }]);
  vi.mocked(api.get).mockImplementation(((url: string) => {
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features } });
    if (url === '/postgres/catalog') return Promise.resolve({ data: CATALOG });
    if (url === '/tiers') return Promise.resolve({ data: [] });
    if (url === '/orgs/org-1/plan-budget') return Promise.resolve({ data: planBudget() });
    return Promise.reject(new Error(`unexpected GET ${url}`));
  }) as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><MemoryRouter><ProvisionPage /></MemoryRouter></QueryClientProvider>);
}

describe('ProvisionPage and the plan budget', () => {
  beforeEach(() => vi.clearAllMocks());

  test('names what is left once an organization is chosen, and shows the budget refusal inline', async () => {
    const user = userEvent.setup();
    mount({ planbudget: true });
    vi.mocked(api.post).mockRejectedValue(new AxiosError('x', 'ERR_BAD_REQUEST', undefined, undefined,
      { status: 409, data: { code: 'plan_budget_exceeded', error: REFUSAL, status: 409 } } as never));
    await screen.findByTestId('pg-version-16');
    await user.selectOptions(await screen.findByLabelText(/organization/i), 'org-1');
    expect(await screen.findByText(/Left in your plan: 1.5 vCPU, 3 GiB memory, 15 GiB disk/)).toBeInTheDocument();
    await user.type(screen.getByLabelText('Project Name'), 'my-db');
    await user.click(screen.getByTestId('pg-version-16'));
    await user.click(screen.getByTestId('provision-submit'));
    expect(await screen.findByTestId('provision-error')).toHaveTextContent(REFUSAL);
  });

  test('says nothing about the plan budget while the feature is off', async () => {
    const user = userEvent.setup();
    mount({});
    await screen.findByTestId('pg-version-16');
    await user.selectOptions(await screen.findByLabelText(/organization/i), 'org-1');
    expect(await screen.findByText(/Projects take their organization/)).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith('/config');
    expect(screen.queryByText(/Left in your plan/)).toBeNull();
  });
});
