import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AdvisorsPage } from './AdvisorsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

const REPORT = {
  engine: 'mysql',
  findings: [
    {
      check: 'anon_unfiltered_access', category: 'security', level: 'error', object: 'app.orders', role: 'anon', operation: 'delete',
      message: 'Anyone, without signing in, can delete every row of app.orders.', fixHint: "Remove anon's delete permission.",
    },
    {
      check: 'duplicate_index', category: 'performance', level: 'warning', object: 'app.items', index: 'items_sku_b',
      message: 'Index items_sku_b on app.items indexes the same columns as items_sku_a.', fixHint: 'Drop items_sku_b and keep items_sku_a.',
      fixSql: 'ALTER TABLE `app`.`items` DROP INDEX `items_sku_b`;',
    },
  ],
  checks: [
    { id: 'fk_without_index', category: 'performance', status: 'not_applicable', reason: 'InnoDB indexes every foreign key itself.' },
    { id: 'unused_index', category: 'performance', status: 'unavailable', reason: "the project's admin login may not read performance_schema." },
    { id: 'duplicate_index', category: 'performance', status: 'ran' },
    { id: 'anon_unfiltered_access', category: 'security', status: 'ran' },
  ],
};

function renderPage(features: Record<string, boolean>, report: unknown = REPORT) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/config') return { data: { deploymentMode: 'cloud', features } } as never;
    if (url === '/provision/p-1/advisors') return { data: report } as never;
    if (url.startsWith('/schema/p-1/advisors/')) return { data: [] } as never;
    throw new Error(`unexpected GET ${url}`);
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p-1/database/advisors']}>
        <Routes>
          <Route path="/project/:projectId/database/advisors" element={<AdvisorsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function requested(url: string) {
  return vi.mocked(api.get).mock.calls.some(([called]) => called === url);
}

describe('Advisors (EXC-655)', () => {
  beforeEach(() => vi.clearAllMocks());

  test('with the feature on, the security findings of the project are listed with their hint', async () => {
    renderPage({ advisors: true });
    const finding = await screen.findByTestId('advisor-finding-anon_unfiltered_access');
    expect(finding).toHaveTextContent('app.orders');
    expect(finding).toHaveTextContent('anon delete');
    expect(finding).toHaveTextContent("Remove anon's delete permission.");
    expect(within(finding).getByText('error')).toBeInTheDocument();
    expect(requested('/schema/p-1/advisors/security')).toBe(false);
  });

  test('a performance finding shows its suggested SQL, never applied', async () => {
    renderPage({ advisors: true });
    await userEvent.click(await screen.findByTestId('tab-performance'));
    const finding = await screen.findByTestId('advisor-finding-duplicate_index');
    expect(within(finding).getByTestId('advisor-fix-sql')).toHaveTextContent('ALTER TABLE `app`.`items` DROP INDEX `items_sku_b`;');
    expect(screen.getByText(/never run for you/i)).toBeInTheDocument();
  });

  test('a check that could not run says why instead of reading as clean', async () => {
    renderPage({ advisors: true });
    await userEvent.click(await screen.findByTestId('tab-performance'));
    const skipped = await screen.findByTestId('advisor-check-unused_index');
    expect(skipped).toHaveTextContent(/not run/i);
    expect(skipped).toHaveTextContent('performance_schema');
    expect(screen.getByTestId('advisor-check-fk_without_index')).toHaveTextContent(/does not apply/i);
  });

  test('no findings is said plainly', async () => {
    renderPage({ advisors: true }, { engine: 'postgres', findings: [], checks: [] });
    expect(await screen.findByText(/no security findings/i)).toBeInTheDocument();
  });

  test('re-running asks the server again', async () => {
    renderPage({ advisors: true });
    await screen.findByTestId('advisor-finding-anon_unfiltered_access');
    await userEvent.click(screen.getByTestId('rescan-btn'));
    await waitFor(() =>
      expect(vi.mocked(api.get).mock.calls.filter(([url]) => url === '/provision/p-1/advisors').length).toBe(2),
    );
  });

  test('with the feature off, the page is the schema advisor it was', async () => {
    renderPage({});
    expect(await screen.findByText('Database Advisors')).toBeInTheDocument();
    await waitFor(() => expect(requested('/schema/p-1/advisors/performance')).toBe(true));
    expect(requested('/provision/p-1/advisors')).toBe(false);
  });
});
