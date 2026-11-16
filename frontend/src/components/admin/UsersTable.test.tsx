import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { UsersTable } from './UsersTable';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const active = {
  id: 'u1', email: 'dev@acme.test', username: 'dev', emailVerified: true, role: 'user', status: 'active',
  orgCount: 1, projectCount: 2, runningApps: 1, tempMail: false, lastLoginAt: '2026-11-15T10:00:00Z', lastLoginIp: '203.0.113.5',
};
const suspended = {
  id: 'u2', email: 'bad@mailinator.test', username: 'bad', emailVerified: false, role: 'user', status: 'suspended',
  suspendedReason: 'proxy abuse', orgCount: 1, projectCount: 1, runningApps: 0, tempMail: true,
};

function renderTable() {
  vi.mocked(api.get).mockImplementation((_url: string, rawConfig?: unknown) => {
    const config = rawConfig as { params?: { includeSuspended?: string; q?: string } } | undefined;
    const items = config?.params?.includeSuspended ? [active, suspended] : [active];
    const query = config?.params?.q;
    return Promise.resolve({ data: { items: query ? items.filter((u) => u.email.includes(query)) : items, total: items.length } } as never);
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <UsersTable />
    </QueryClientProvider>,
  );
}

describe('UsersTable', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows who each account is: email, temp-mail flag, last sign-in and counts', async () => {
    renderTable();
    const row = (await screen.findByText('dev@acme.test')).closest('tr') as HTMLElement;
    expect(within(row).getByText('203.0.113.5')).toBeInTheDocument();
    expect(within(row).getByText('Active')).toBeInTheDocument();
    expect(within(row).getByText('2')).toBeInTheDocument();
  });

  test('suspended accounts are hidden until the filter asks for them', async () => {
    renderTable();
    await screen.findByText('dev@acme.test');
    expect(screen.queryByText('bad@mailinator.test')).not.toBeInTheDocument();

    await userEvent.click(screen.getByLabelText('Show suspended'));
    const row = (await screen.findByText('bad@mailinator.test')).closest('tr') as HTMLElement;
    expect(within(row).getByText('Suspended')).toBeInTheDocument();
    expect(within(row).getByText('proxy abuse')).toBeInTheDocument();
    expect(within(row).getByText('temp mail')).toBeInTheDocument();
  });

  test('searching sends the text to the server', async () => {
    renderTable();
    await screen.findByText('dev@acme.test');
    await userEvent.type(screen.getByPlaceholderText(/Search email, domain or IP/), 'acme');
    await waitFor(() => expect(vi.mocked(api.get)).toHaveBeenCalledWith('/admin/console/users', expect.objectContaining({ params: expect.objectContaining({ q: 'acme' }) })));
  });

  test('suspending asks for a reason and sends it', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { userId: 'u1', status: 'suspended', appsStopped: [], appsRestored: [], appsFailed: [] } } as never);
    renderTable();
    const row = (await screen.findByText('dev@acme.test')).closest('tr') as HTMLElement;
    await userEvent.click(within(row).getByRole('button', { name: 'Suspend' }));

    const dialog = screen.getByRole('dialog');
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'running an open proxy');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Suspend' }));

    await waitFor(() => expect(vi.mocked(api.post)).toHaveBeenCalledWith('/admin/console/users/u1/suspend', { reason: 'running an open proxy' }));
  });

  test('unsuspending a suspended account also asks for a reason', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { userId: 'u2', status: 'active', appsStopped: [], appsRestored: [], appsFailed: [] } } as never);
    renderTable();
    await screen.findByText('dev@acme.test');
    await userEvent.click(screen.getByLabelText('Show suspended'));
    const row = (await screen.findByText('bad@mailinator.test')).closest('tr') as HTMLElement;
    await userEvent.click(within(row).getByRole('button', { name: 'Unsuspend' }));
    await userEvent.type(within(screen.getByRole('dialog')).getByLabelText('Reason'), 'appealed, verified by email');
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Unsuspend' }));

    await waitFor(() => expect(vi.mocked(api.post)).toHaveBeenCalledWith('/admin/console/users/u2/unsuspend', { reason: 'appealed, verified by email' }));
  });

  test('an app that would not stop is reported after a suspension', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { userId: 'u1', status: 'suspended', appsStopped: [], appsRestored: [], appsFailed: [{ appName: 'proxy', reason: 'cluster busy' }] } } as never);
    renderTable();
    const row = (await screen.findByText('dev@acme.test')).closest('tr') as HTMLElement;
    await userEvent.click(within(row).getByRole('button', { name: 'Suspend' }));
    await userEvent.type(within(screen.getByRole('dialog')).getByLabelText('Reason'), 'running an open proxy');
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Suspend' }));

    expect(await screen.findByRole('status')).toHaveTextContent('proxy');
    expect(screen.getByRole('status')).toHaveTextContent('cluster busy');
  });
});
