import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { DatabaseRunning } from './DatabaseRunning';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

function renderWith(project: Record<string, unknown>) {
  vi.mocked(api.get).mockResolvedValue({ data: { projectId: 'proj-1', ...project } } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/database/tables']}>
        <Routes>
          <Route path="/project/:projectId" element={<DatabaseRunning />}>
            <Route path="database/tables" element={<p>tables page</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// EXC-555: the server only serves an ACTIVE project's database and answers
// every other state with 409, which left these pages on a skeleton forever.
describe('DatabaseRunning', () => {
  beforeEach(() => vi.clearAllMocks());

  test('an active project opens the page', async () => {
    renderWith({ status: 'ACTIVE' });
    expect(await screen.findByText('tables page')).toBeInTheDocument();
  });

  test.each([
    [{ status: 'PROVISIONING', currentStage: 'CREATING_DATABASE' }, /being set up/, true],
    [{ status: '' }, /being set up/, true],
    [{ status: 'PAUSED' }, /paused/, false],
    [{ status: 'PAUSING' }, /pausing/, true],
    [{ status: 'RESUMING' }, /resuming/, true],
    [{ status: 'RESTORING' }, /being restored/, true],
    [{ status: 'PENDING_DELETION' }, /scheduled for deletion/, false],
    [{ status: 'FAILED' }, /could not be created/, false],
    [{ status: 'DELETING' }, /deleted/, false],
    [{ status: 'DEPROVISIONED' }, /deleted/, false],
    [{ status: 'SOMETHING_NEW' }, /not running \(SOMETHING_NEW\)/, false],
  ])('%o explains itself instead of opening the page', async (project, title, waits) => {
    renderWith(project);
    const panel = await screen.findByTestId('database-not-running');
    expect(panel).toHaveTextContent(title);
    expect(screen.queryByText('tables page')).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: /overview/i })).toHaveAttribute('href', '/project/proj-1');
    expect(panel.querySelector('.animate-spin') !== null).toBe(waits);
  });
});
