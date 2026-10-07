import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { RestoreForm } from './RestoreForm';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const IN_PLACE_JOB = {
  id: 'job-9',
  sourceProjectId: 'p1',
  newProjectId: 'p1',
  mode: 'in_place',
  status: 'RUNNING',
  targetKind: 'time',
};
const LIMIT = {
  message: 'Request failed with status code 409',
  response: {
    status: 409,
    data: {
      error: 'organization has reached its project limit of 1 for the FREE tier; restore into this project instead, which replaces its current data',
      code: 'project_limit_reached',
    },
  },
};

function stubJob(job: Record<string, unknown>) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url.startsWith('/provision/p1/backup/restore/')) return Promise.resolve({ data: job } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RestoreForm projectId="p1" />
    </QueryClientProvider>,
  );
}

async function openRestoreTab() {
  renderPage();
}

describe('RestoreForm on a plan at its project limit', () => {
  beforeEach(() => vi.clearAllMocks());

  test('the refusal stays on screen and offers restoring into this project', async () => {
    stubJob(IN_PLACE_JOB);
    vi.mocked(api.post).mockRejectedValueOnce(LIMIT);
    await openRestoreTab();
    await userEvent.type(screen.getByLabelText(/New project name/i), 'copy');
    await userEvent.click(screen.getByRole('button', { name: /Restore Latest Backup/i }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('project limit of 1');
    await userEvent.click(within(alert).getByRole('button', { name: /Restore into this project instead/i }));

    expect(screen.getByRole('radio', { name: /This project/i })).toBeChecked();
    expect(screen.queryByLabelText(/New project name/i)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Replace this project's data/i })).toBeDisabled();
  });

  test('restoring into this project asks for consent, then sends the replace once', async () => {
    stubJob({ ...IN_PLACE_JOB, currentStep: 'SAFETY_BACKUP' });
    vi.mocked(api.post).mockResolvedValueOnce({ data: IN_PLACE_JOB } as never);
    await openRestoreTab();
    await userEvent.click(screen.getByRole('radio', { name: /This project/i }));
    const submit = screen.getByRole('button', { name: /Replace this project's data/i });
    expect(submit).toBeDisabled();

    await userEvent.click(screen.getByRole('checkbox', { name: /I understand/i }));
    await userEvent.click(submit);

    expect(api.post).toHaveBeenCalledWith('/provision/p1/backup/restore', {
      mode: 'in_place',
      confirmReplace: true,
      targetTime: undefined,
    });
    const result = await screen.findByTestId('restore-result');
    expect(result).toHaveTextContent(/Taking a backup of the current data first/);
    expect(screen.getByRole('button', { name: /Restore running/i })).toBeDisabled();
    await userEvent.click(screen.getByRole('button', { name: /Restore running/i }));
    expect(api.post).toHaveBeenCalledTimes(1);
  });

  test('a completed replace says the project now holds the restored data', async () => {
    stubJob({ ...IN_PLACE_JOB, status: 'COMPLETED' });
    vi.mocked(api.post).mockResolvedValueOnce({ data: IN_PLACE_JOB } as never);
    await openRestoreTab();
    await userEvent.click(screen.getByRole('radio', { name: /This project/i }));
    await userEvent.click(screen.getByRole('checkbox', { name: /I understand/i }));
    await userEvent.click(screen.getByRole('button', { name: /Replace this project's data/i }));

    expect(await screen.findByText(/This project's data has been restored/)).toBeInTheDocument();
    expect(screen.queryByText(/New instance/)).not.toBeInTheDocument();
  });

  test('a rolled-back replace shows its reason', async () => {
    stubJob({ ...IN_PLACE_JOB, status: 'FAILED', failureReason: 'the restore did not complete, so the project was put back as it was just before it (backup b-1)' });
    vi.mocked(api.post).mockResolvedValueOnce({ data: IN_PLACE_JOB } as never);
    await openRestoreTab();
    await userEvent.click(screen.getByRole('radio', { name: /This project/i }));
    await userEvent.click(screen.getByRole('checkbox', { name: /I understand/i }));
    await userEvent.click(screen.getByRole('button', { name: /Replace this project's data/i }));

    expect(await screen.findByText(/put back as it was just before it/)).toBeInTheDocument();
  });
});

describe('RestoreForm progress', () => {
  beforeEach(() => vi.clearAllMocks());

  test('an internal step name is never shown', async () => {
    stubJob({ id: 'job-1', status: 'RUNNING', newProjectId: 'proj-new', currentStep: 'delegate-to-adapter' });
    vi.mocked(api.post).mockResolvedValueOnce({ data: { id: 'job-1', status: 'RUNNING', newProjectId: 'proj-new' } } as never);
    await openRestoreTab();
    await userEvent.type(screen.getByLabelText(/New project name/i), 'copy');
    await userEvent.click(screen.getByRole('button', { name: /Restore Latest Backup/i }));

    const result = await screen.findByTestId('restore-result');
    expect(result).toHaveTextContent(/Restore started/);
    expect(document.body).not.toHaveTextContent('delegate-to-adapter');
  });

  test('a copy that is running cannot be started again', async () => {
    stubJob({ id: 'job-1', status: 'RUNNING', newProjectId: 'proj-new', currentStep: 'RESTORING_DATABASE' });
    vi.mocked(api.post).mockResolvedValueOnce({ data: { id: 'job-1', status: 'RUNNING', newProjectId: 'proj-new' } } as never);
    await openRestoreTab();
    await userEvent.type(screen.getByLabelText(/New project name/i), 'copy');
    await userEvent.click(screen.getByRole('button', { name: /Restore Latest Backup/i }));

    await screen.findByTestId('restore-result');
    expect(screen.getByRole('button', { name: /Restore running/i })).toBeDisabled();
    expect(screen.getByTestId('restore-result')).toHaveTextContent(/Restoring the database/);
  });

  test('a duplicate the server refuses is explained', async () => {
    stubJob(IN_PLACE_JOB);
    vi.mocked(api.post).mockRejectedValueOnce({
      response: { status: 409, data: { error: 'a restore of this project is already running; wait for it to finish' } },
    });
    await openRestoreTab();
    await userEvent.type(screen.getByLabelText(/New project name/i), 'copy');
    await userEvent.click(screen.getByRole('button', { name: /Restore Latest Backup/i }));

    expect(await screen.findByRole('alert')).toHaveTextContent('already running');
    expect(screen.queryByRole('button', { name: /Restore into this project instead/i })).not.toBeInTheDocument();
  });
});
