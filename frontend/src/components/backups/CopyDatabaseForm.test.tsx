import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { CopyDatabaseForm } from './CopyDatabaseForm';
import { api } from '../../api/client';
import { toLocalInput } from '../../utils/zonedInstant';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

// EXC-667: copy this database into a new project, as it is now, from its
// latest backup, or at a time.

const RUNNING = { id: 'job-1', sourceProjectId: 'p1', newProjectId: 'copy-1', mode: 'new_project', status: 'RUNNING', targetKind: 'now' };

function stubJob(job: Record<string, unknown>) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p1/backup/restore/job-1') return Promise.resolve({ data: job } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderForm(props: Partial<Parameters<typeof CopyDatabaseForm>[0]> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <CopyDatabaseForm projectId="p1" {...props} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const submit = () => screen.getByRole('button', { name: /Copy database/i });

describe('CopyDatabaseForm', () => {
  beforeEach(() => vi.clearAllMocks());

  test('copies the database as it is now by default, once named', async () => {
    stubJob(RUNNING);
    vi.mocked(api.post).mockResolvedValueOnce({ data: RUNNING } as never);
    renderForm();

    expect(screen.getByRole('radio', { name: /As it is now/i })).toBeChecked();
    expect(screen.queryByLabelText(/Copy at/i)).not.toBeInTheDocument();
    expect(submit()).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/New project name/i), 'staging');
    await userEvent.click(submit());

    expect(api.post).toHaveBeenCalledWith('/provision/p1/backup/copy', { name: 'staging', point: 'now' });
  });

  test('copies the latest backup', async () => {
    stubJob(RUNNING);
    vi.mocked(api.post).mockResolvedValueOnce({ data: RUNNING } as never);
    renderForm();

    await userEvent.click(screen.getByRole('radio', { name: /Latest backup/i }));
    await userEvent.type(screen.getByLabelText(/New project name/i), 'nightly');
    await userEvent.click(submit());

    expect(api.post).toHaveBeenCalledWith('/provision/p1/backup/copy', { name: 'nightly', point: 'latest' });
  });

  test('a copy at a time needs the time and sends it with its zone', async () => {
    stubJob(RUNNING);
    vi.mocked(api.post).mockResolvedValueOnce({ data: RUNNING } as never);
    renderForm();

    await userEvent.click(screen.getByRole('radio', { name: /A point in time/i }));
    await userEvent.type(screen.getByLabelText(/New project name/i), 'before');
    expect(submit()).toBeDisabled();
    const at = '2026-10-29T10:00:00.000Z';
    await userEvent.type(screen.getByLabelText(/Copy at/i), toLocalInput(at));
    await userEvent.click(submit());

    const [url, body] = vi.mocked(api.post).mock.calls[0] as [string, { name: string; point: string; targetTime: string }];
    expect(url).toBe('/provision/p1/backup/copy');
    expect(body.point).toBe('time');
    expect(new Date(body.targetTime).toISOString()).toBe(at);
  });

  test('a MySQL copy at a time is bounded by the backup window', async () => {
    const window = { earliest: '2026-10-11T00:00:00Z', latest: '2026-10-11T12:00:00Z' };
    renderForm({ engine: 'mysql', restoreWindow: window });
    await userEvent.click(screen.getByRole('radio', { name: /A point in time/i }));

    const field = screen.getByLabelText(/Copy at/i);
    expect(field).toHaveAttribute('min', toLocalInput(window.earliest));
    expect(field).toHaveAttribute('max', toLocalInput(window.latest));
  });

  test('says that logins created inside the database keep their passwords', () => {
    renderForm();
    expect(screen.getByTestId('copy-logins-note')).toHaveTextContent(/keep their passwords/i);
  });

  test('a refusal is shown as the server words it', async () => {
    vi.mocked(api.post).mockRejectedValueOnce({
      message: 'Request failed with status code 409',
      response: { status: 409, data: { error: 'copies are made from backups; turn on backups for this database first' } },
    });
    renderForm();
    await userEvent.type(screen.getByLabelText(/New project name/i), 'x');
    await userEvent.click(submit());

    expect(await screen.findByRole('alert')).toHaveTextContent('turn on backups for this database first');
  });

  test('reports the backup taken for the copy, then links the finished copy', async () => {
    stubJob({ ...RUNNING, currentStep: 'SOURCE_BACKUP' });
    vi.mocked(api.post).mockResolvedValueOnce({ data: RUNNING } as never);
    renderForm();
    await userEvent.type(screen.getByLabelText(/New project name/i), 'staging');
    await userEvent.click(submit());

    expect(await screen.findByText(/Taking a backup of the database as it is now/i)).toBeInTheDocument();
    expect(submit()).toBeDisabled();
  });

  test('links the copy once it is ready', async () => {
    stubJob({ ...RUNNING, status: 'COMPLETED' });
    vi.mocked(api.post).mockResolvedValueOnce({ data: RUNNING } as never);
    renderForm();
    await userEvent.type(screen.getByLabelText(/New project name/i), 'staging');
    await userEvent.click(submit());

    const link = await screen.findByRole('link', { name: /Open the copy/i });
    expect(link).toHaveAttribute('href', '/project/copy-1');
  });

  test('a failed copy says why', async () => {
    stubJob({ ...RUNNING, status: 'FAILED', failureReason: 'the backup taken for the copy did not complete, so no copy was made; try again' });
    vi.mocked(api.post).mockResolvedValueOnce({ data: RUNNING } as never);
    renderForm();
    await userEvent.type(screen.getByLabelText(/New project name/i), 'staging');
    await userEvent.click(submit());

    expect(await screen.findByText(/The copy failed: the backup taken for the copy did not complete/i)).toBeInTheDocument();
  });
});
