import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { BackupsPage } from './BackupsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const LIST = { backups: [], backupEnabled: true, schedule: '', retentionDays: 7 };
const STARTED = {
  id: 'job-1',
  sourceProjectId: 'p1',
  newProjectId: 'proj-new',
  newProjectName: 'copy',
  status: 'RUNNING',
  targetKind: 'latest',
};

function stubJob(job: Record<string, unknown>) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p1/backup/list') return Promise.resolve({ data: LIST } as never);
    if (url === '/provision/p1/backup/restore/job-1')
      return Promise.resolve({ data: job } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p1/backups']}>
        <Routes>
          <Route path="/project/:projectId/backups" element={<BackupsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function startRestore() {
  vi.mocked(api.post).mockResolvedValueOnce({ data: STARTED } as never);
  renderPage();
  await userEvent.click(screen.getByRole('button', { name: /Restore \/ PITR/i }));
  await userEvent.type(screen.getByLabelText(/New Instance Name/i), 'copy');
  await userEvent.click(screen.getByRole('button', { name: /Restore Latest Backup/i }));
}

describe('BackupsPage restore job', () => {
  beforeEach(() => vi.clearAllMocks());

  // The server answers a started restore with RUNNING; that is a start, not a failure.
  test('a started restore reads as started and polls its job', async () => {
    stubJob({ ...STARTED, currentStep: 'creating the cluster' });
    await startRestore();
    const result = await screen.findByTestId('restore-result');
    expect(result).toHaveTextContent(/Restore started/);
    expect(result).toHaveTextContent('proj-new');
    expect(result).toHaveAttribute('data-tone', 'pending');
    expect(api.get).toHaveBeenCalledWith('/provision/p1/backup/restore/job-1');
  });

  test("a failed restore shows the job's reason", async () => {
    stubJob({ ...STARTED, status: 'FAILED', failureReason: 'the base backup is missing' });
    await startRestore();
    expect(await screen.findByText(/the base backup is missing/)).toBeInTheDocument();
    expect(screen.getByTestId('restore-result')).toHaveAttribute('data-tone', 'error');
  });

  test('a completed restore says so', async () => {
    stubJob({ ...STARTED, status: 'COMPLETED' });
    await startRestore();
    expect(await screen.findByText(/Restore completed/)).toBeInTheDocument();
    expect(screen.getByTestId('restore-result')).toHaveAttribute('data-tone', 'ok');
  });
});

describe('BackupsPage trigger backup', () => {
  beforeEach(() => vi.clearAllMocks());

  test("a refused backup shows the server's reason", async () => {
    stubJob(STARTED);
    vi.mocked(api.post).mockRejectedValueOnce({
      message: 'Request failed with status code 409',
      response: { status: 409, data: { error: 'a backup is already running for this project' } },
    });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /Trigger Backup/i }));
    expect(
      await screen.findByText('a backup is already running for this project'),
    ).toBeInTheDocument();
    expect(api.post).toHaveBeenCalledWith('/provision/p1/backup/trigger');
  });
});
