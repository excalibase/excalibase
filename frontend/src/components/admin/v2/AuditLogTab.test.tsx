import { beforeEach, describe, expect, test, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { api } from '../../../api/client';
import { AuditLogTab } from './AuditLogTab';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn() } }));

const WHEN = '2026-11-27T10:00:00Z';
const row = (id: number, extra: object = {}) => ({
  id: String(id), at: WHEN, actorId: 'boss', actorEmail: 'boss@x.test', action: 'user.suspend',
  resource: 'user', resourceId: `u${id}`, targetEmail: `target${id}@x.test`, details: `reason ${id}`, ...extra,
});

function renderTab() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><MemoryRouter><AuditLogTab /></MemoryRouter></QueryClientProvider>);
}

const asked = () => vi.mocked(api.get).mock.calls.map(([, config]) => (config as { params: Record<string, unknown> }).params);

describe('AuditLogTab', () => {
  beforeEach(() => vi.clearAllMocks());

  test('lists who did what to which target and when, once the page has loaded', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { items: [row(2), row(1)] } } as never);
    renderTab();
    expect(await screen.findByText('target2@x.test')).toBeInTheDocument();
    expect(screen.getAllByText('boss@x.test')).toHaveLength(2);
    expect(screen.getAllByText('user.suspend')).toHaveLength(2);
    expect(screen.getByText('reason 1')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'target2@x.test' })).toHaveAttribute('href', '/admin/users/u2');
  });

  test('a target that is not an account shows its kind and id', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { items: [row(3, { resource: 'project', resourceId: 'p-9', targetEmail: undefined })] } } as never);
    renderTab();
    expect(await screen.findByText('project p-9')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /p-9/ })).toBeNull();
  });

  test('Load more asks for the next page by the cursor and appends it, then says it is the end', async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({ data: { items: [row(2)], nextCursor: 'c1' } } as never)
      .mockResolvedValueOnce({ data: { items: [row(1)] } } as never);
    renderTab();
    await screen.findByText('target2@x.test');
    expect(screen.queryByText(/That's all/)).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }));
    expect(await screen.findByText('target1@x.test')).toBeInTheDocument();
    expect(screen.getByText('target2@x.test')).toBeInTheDocument();
    expect(asked()[1]).toMatchObject({ cursor: 'c1' });
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull();
    expect(screen.getByText("That's all (2)")).toBeInTheDocument();
  });

  test('a filter goes to the server and starts again from the newest', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { items: [row(1)] } } as never);
    renderTab();
    await screen.findByText('target1@x.test');
    await userEvent.type(screen.getByRole('textbox', { name: 'Action' }), 'user.');
    await waitFor(() => expect(asked().at(-1)).toMatchObject({ action: 'user.' }));
    expect(asked().at(-1)).not.toHaveProperty('cursor');
    await userEvent.type(screen.getByRole('textbox', { name: 'Who' }), 'boss@x.test');
    await waitFor(() => expect(asked().at(-1)).toMatchObject({ action: 'user.', actor: 'boss@x.test' }));
    await userEvent.type(screen.getByRole('textbox', { name: 'Target' }), 'u1');
    await waitFor(() => expect(asked().at(-1)).toMatchObject({ target: 'u1' }));
  });

  test('an empty log says so, and a failed read can be retried', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { items: [] } } as never);
    const { unmount } = renderTab();
    expect(await screen.findByText('No audit entries match.')).toBeInTheDocument();
    unmount();
    vi.mocked(api.get).mockRejectedValueOnce(new Error('boom')).mockResolvedValueOnce({ data: { items: [row(1)] } } as never);
    renderTab();
    await userEvent.click(await screen.findByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('target1@x.test')).toBeInTheDocument();
  });
});
