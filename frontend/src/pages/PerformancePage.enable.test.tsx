import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PerformancePage } from './PerformancePage';

const mutate = vi.fn();
const missing = { response: { data: { hint: 'enable pg_stat_statements' } } };
const idle = { data: undefined, isLoading: false, isError: false, error: null };
const failed = { data: undefined, isLoading: false, isError: true, error: missing };

vi.mock('../hooks/useRouteProjectId', () => ({ useRouteProjectId: () => 'p-1' }));
vi.mock('../hooks/usePerformance', () => ({
  usePerformanceSummary: () => failed,
  useTopQueries: () => idle,
  useWaitEvents: () => idle,
  useEnablePerformanceInsights: () => ({ mutate, isPending: false }),
}));

// EXC-850: enabling Performance Insights talks about the database, not the cluster.
describe('PerformancePage — enable gate', () => {
  beforeEach(() => vi.clearAllMocks());

  test('the helper text says database restart', () => {
    render(<PerformancePage />);
    expect(screen.getByText('Triggers a rolling database restart — takes ~30 seconds. No data loss.')).toBeInTheDocument();
  });

  test('the default toast says database restart', async () => {
    mutate.mockImplementation((_v, o) => o.onSuccess({}));
    render(<PerformancePage />);
    await userEvent.click(screen.getByRole('button', { name: 'Enable pg_stat_statements' }));
    expect(await screen.findByText('Enabled — database restart in progress (~30s)')).toBeInTheDocument();
  });

  test('a status from the server is shown as is', async () => {
    mutate.mockImplementation((_v, o) => o.onSuccess({ status: 'Restarting now' }));
    render(<PerformancePage />);
    await userEvent.click(screen.getByRole('button', { name: 'Enable pg_stat_statements' }));
    expect(await screen.findByText('Restarting now')).toBeInTheDocument();
  });
});
