import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { PerformancePage } from './PerformancePage';

function sent(name: string) {
  return JSON.parse(readFileSync(resolve(process.cwd(), '../shared-fixtures/wire', name), 'utf8'));
}

const summary = { ...sent('performance-summary.json'), available: true, unavailableReason: undefined };
type SummaryResult = { data: unknown; isLoading: boolean; isError: boolean; error: unknown };
const available: SummaryResult = { data: summary, isLoading: false, isError: false, error: null };
let summaryResult: SummaryResult = available;
const queries = { data: [sent('query-stat.json')], isLoading: false, isError: false, error: null };
const waits = { data: [sent('wait-event.json')], isLoading: false, isError: false, error: null };

vi.mock('../hooks/useRouteProjectId', () => ({ useRouteProjectId: () => 'p-1' }));
vi.mock('../hooks/usePerformance', () => ({
  usePerformanceSummary: () => summaryResult,
  useTopQueries: () => queries,
  useWaitEvents: () => waits,
  useEnablePerformanceInsights: () => ({ mutate: vi.fn(), isPending: false }),
}));

// EXC-893: the page read fields the server never sends and showed blanks.
describe('PerformancePage shows what the server sends', () => {
  beforeEach(() => {
    summaryResult = available;
  });

  test('the overview shows the size and the slow query count', () => {
    render(<PerformancePage />);
    expect(screen.getByText('48 MB')).toBeInTheDocument();
    expect(screen.getByText('3 / 12')).toBeInTheDocument();
    expect(screen.getByText('99.4%')).toBeInTheDocument();
    expect(screen.getByText('2')).toBeInTheDocument();
  });

  test('top queries show total and average time', async () => {
    render(<PerformancePage />);
    await userEvent.click(screen.getByRole('button', { name: /Top Queries/ }));
    expect(screen.getByText('840.5ms')).toBeInTheDocument();
    expect(screen.getByText('7.0ms')).toBeInTheDocument();
  });

  test('wait events show their type, event and count', async () => {
    render(<PerformancePage />);
    await userEvent.click(screen.getByRole('button', { name: /Wait Events/ }));
    expect(screen.getByText('IO')).toBeInTheDocument();
    expect(screen.getByText('DataFileRead')).toBeInTheDocument();
    expect(screen.getByText('4')).toBeInTheDocument();
  });

  test('an extension that is on shows no enable gate', () => {
    render(<PerformancePage />);
    expect(screen.getByText('48 MB')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Enable pg_stat_statements' })).not.toBeInTheDocument();
  });

  test('a database that cannot be asked shows the error and no enable gate', () => {
    summaryResult = { data: undefined, isLoading: false, isError: true, error: { response: { data: { error: 'the database is not reachable' } } } };
    render(<PerformancePage />);
    expect(screen.getByText('Performance data unavailable')).toBeInTheDocument();
    expect(screen.getByText('the database is not reachable')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Enable pg_stat_statements' })).not.toBeInTheDocument();
  });

  test('an extension that is off shows the enable gate', () => {
    summaryResult = { data: sent('performance-summary.json'), isLoading: false, isError: false, error: null };
    render(<PerformancePage />);
    expect(screen.getByRole('button', { name: 'Enable pg_stat_statements' })).toBeInTheDocument();
  });
});
