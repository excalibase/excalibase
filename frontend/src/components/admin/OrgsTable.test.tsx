import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { OrgsTable } from './OrgsTable';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), delete: vi.fn(), patch: vi.fn() } }));

const orgs = [
  { id: 'o1', name: 'Acme', slug: 'acme', tier: 'free', ownerId: 'u1', ownerEmail: 'owner@acme.test', ownerStatus: 'active', projectCount: 2, runningApps: 1, cpuCores: 0.5, memoryMb: 300 },
  { id: 'o2', name: 'Spam', slug: 'spam', tier: 'free', ownerId: 'u2', ownerEmail: 'bad@mailinator.test', ownerStatus: 'suspended', projectCount: 1, runningApps: 0, cpuCores: 0, memoryMb: 0 },
];

type Cfg = { params?: { includeSuspended?: string } };

function renderTable(canMutate = true) {
  vi.mocked(api.get).mockImplementation((_url: string, rawConfig?: unknown) =>
    Promise.resolve({ data: { items: (rawConfig as Cfg | undefined)?.params?.includeSuspended ? orgs : orgs.slice(0, 1), total: 2 } } as never),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <OrgsTable canMutate={canMutate} />
    </QueryClientProvider>,
  );
}

describe('OrgsTable', () => {
  beforeEach(() => vi.clearAllMocks());

  test('names the owner by email and shows usage, not hex ids', async () => {
    renderTable();
    const row = (await screen.findByText('Acme')).closest('tr') as HTMLElement;
    expect(within(row).getByText('owner@acme.test')).toBeInTheDocument();
    expect(within(row).getByText('Active')).toBeInTheDocument();
    expect(within(row).getByText('0.50 cores · 300 MiB')).toBeInTheDocument();
    expect(within(row).queryByText('o1')).not.toBeInTheDocument();
  });

  test('an org of a suspended owner is hidden until asked for, then labelled Suspended', async () => {
    renderTable();
    await screen.findByText('Acme');
    expect(screen.queryByText('Spam')).not.toBeInTheDocument();
    await userEvent.click(screen.getByLabelText('Show suspended'));
    const row = (await screen.findByText('Spam')).closest('tr') as HTMLElement;
    expect(within(row).getByText('Suspended')).toBeInTheDocument();
  });

  test('revoke stays disabled for a read-only operator', async () => {
    renderTable(false);
    const row = (await screen.findByText('Acme')).closest('tr') as HTMLElement;
    expect(within(row).getByRole('button', { name: /Revoke/ })).toBeDisabled();
  });

  test('revoke needs a written reason, sends it, and says the owner is suspended', async () => {
    renderTable();
    const row = (await screen.findByText('Acme')).closest('tr') as HTMLElement;
    await userEvent.click(within(row).getByRole('button', { name: /Revoke/ }));
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveTextContent('suspends its owner');
    expect(dialog).toHaveTextContent('owner@acme.test');
    const confirmButton = within(dialog).getByRole('button', { name: 'Revoke' });
    expect(confirmButton).toBeDisabled();
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'proxy abuse on every project');
    await userEvent.click(confirmButton);
    expect(api.delete).toHaveBeenCalledWith('/admin/orgs/o1', { params: { cascade: true }, data: { reason: 'proxy abuse on every project' } });
  });

  test("a refused revoke shows the server's reason in the dialog and keeps it open", async () => {
    vi.mocked(api.delete).mockRejectedValueOnce({ isAxiosError: true, response: { data: { error: 'the org owns the platform' } } });
    renderTable();
    const row = (await screen.findByText('Acme')).closest('tr') as HTMLElement;
    await userEvent.click(within(row).getByRole('button', { name: /Revoke/ }));
    const dialog = screen.getByRole('dialog');
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'proxy abuse on every project');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Revoke' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('the org owns the platform');
  });

  test('change plan needs a reason, sends the chosen tier with it, and is off for an operator', async () => {
    vi.mocked(api.patch).mockResolvedValueOnce({ data: {} } as never);
    renderTable();
    const row = (await screen.findByText('Acme')).closest('tr') as HTMLElement;
    await userEvent.click(within(row).getByRole('button', { name: /Change plan/ }));
    const dialog = screen.getByRole('dialog');
    await userEvent.selectOptions(within(dialog).getByLabelText('Plan'), 'STANDARD');
    const confirmButton = within(dialog).getByRole('button', { name: 'Change plan' });
    expect(confirmButton).toBeDisabled();
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'agreed upgrade, invoice 42');
    await userEvent.click(confirmButton);
    expect(api.patch).toHaveBeenCalledWith('/admin/console/orgs/o1/tier', { tier: 'STANDARD', reason: 'agreed upgrade, invoice 42' });
  });

  test("a refused plan change shows the server's reason in the dialog", async () => {
    vi.mocked(api.patch).mockRejectedValueOnce({ isAxiosError: true, response: { data: { error: 'a Standard plan needs 3 nodes' } } });
    renderTable();
    const row = (await screen.findByText('Acme')).closest('tr') as HTMLElement;
    await userEvent.click(within(row).getByRole('button', { name: /Change plan/ }));
    const dialog = screen.getByRole('dialog');
    await userEvent.selectOptions(within(dialog).getByLabelText('Plan'), 'STANDARD');
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'agreed upgrade, invoice 42');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Change plan' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('a Standard plan needs 3 nodes');
  });

  test('change plan stays disabled for a read-only operator', async () => {
    renderTable(false);
    const row = (await screen.findByText('Acme')).closest('tr') as HTMLElement;
    expect(within(row).getByRole('button', { name: /Change plan/ })).toBeDisabled();
  });
});
