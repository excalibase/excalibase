import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StorageBudgetCard } from './StorageBudgetCard';
import { api } from '../api/client';
import type { StorageBudget } from '../hooks/useAdmin';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

const GI = 1024 ** 3;

const report: StorageBudget = {
  enabled: true,
  capacityBytes: 100 * GI,
  percent: 80,
  budgetBytes: 80 * GI,
  allocatedBytes: 40 * GI,
  tenantBytes: 30 * GI,
  platformBytes: 8 * GI,
  pendingBytes: 2 * GI,
  freeBytes: 40 * GI,
  usedPercent: 50,
};

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <StorageBudgetCard />
    </QueryClientProvider>,
  );
}

function renderReport(overrides: Partial<StorageBudget>) {
  vi.mocked(api.get).mockResolvedValue({ data: { ...report, ...overrides } } as never);
  renderCard();
}

describe('StorageBudgetCard', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
  });

  test('shows what volumes reserve against the budget and where it goes', async () => {
    renderReport({});
    expect(await screen.findByTestId('storage-budget-used')).toHaveTextContent(
      '40Gi of 80Gi reserved (50%)',
    );
    expect(api.get).toHaveBeenCalledWith('/admin/storage');
    expect(screen.getByTestId('storage-budget-share')).toHaveTextContent('80% of 100Gi');
    expect(screen.getByTestId('storage-budget-tenants')).toHaveTextContent('30Gi');
    expect(screen.getByTestId('storage-budget-platform')).toHaveTextContent('8Gi');
    expect(screen.getByTestId('storage-budget-pending')).toHaveTextContent('2Gi');
    expect(screen.getByTestId('storage-budget-free')).toHaveTextContent('40Gi');
    expect(screen.getByTestId('storage-budget-bar')).toHaveAttribute('data-level', 'ok');
  });

  test.each([
    [69.9, 'ok'],
    [70, 'warning'],
    [79.9, 'warning'],
    [80, 'critical'],
  ])('%s percent of the budget reads %s', async (usedPercent, level) => {
    renderReport({ usedPercent });
    expect(await screen.findByTestId('storage-budget-bar')).toHaveAttribute('data-level', level);
  });

  test('an unmetered install says it is not metered', async () => {
    renderReport({ enabled: false, budgetBytes: 0, capacityBytes: 0 });
    expect(await screen.findByTestId('storage-budget-unmetered')).toHaveTextContent(/not metered/i);
    expect(screen.queryByTestId('storage-budget-bar')).not.toBeInTheDocument();
  });

  test('an unreadable budget shows the server message', async () => {
    vi.mocked(api.get).mockRejectedValue({
      response: { status: 503, data: { error: "the platform's storage could not be read" } },
    });
    renderCard();
    expect(await screen.findByRole('alert')).toHaveTextContent(
      "the platform's storage could not be read",
    );
  });
});
