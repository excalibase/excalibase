import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AiActivityFeed } from './AiActivityFeed';
import { api } from '../api/client';
import { callResult } from '../api/aiActivity';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

const calls = {
  calls: [
    { id: 3, tool: 'apply_migration', status: 'error', httpStatus: 403, tokenName: 'Cursor MCP', userId: 'me', at: '2026-10-06T01:00:00Z', mine: true, tokenId: 'hash-live' },
    { id: 2, tool: 'list_tables', status: 'ok', tokenName: 'Old MCP', userId: 'me', at: '2026-10-06T00:59:00Z', mine: true, tokenRevoked: true },
    { id: 1, tool: 'execute_sql', status: 'ok', tokenName: 'Their MCP', userId: 'teammate', at: '2026-10-06T00:58:00Z', mine: false },
  ],
};

function renderFeed() {
  vi.mocked(api.get).mockResolvedValue({ data: calls } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AiActivityFeed projectId="proj-1" />
    </QueryClientProvider>,
  );
}

describe('AI activity feed', () => {
  beforeEach(() => vi.clearAllMocks());

  test('lists each call with its tool, token, time and result', async () => {
    renderFeed();
    const row = await screen.findByTestId('ai-activity-3');
    expect(row).toHaveTextContent('apply_migration');
    expect(row).toHaveTextContent('Cursor MCP');
    expect(row).toHaveTextContent('Refused (403)');
    expect(screen.getByTestId('ai-activity-1')).toHaveTextContent('OK');
    expect(api.get).toHaveBeenCalledWith('/projects/proj-1/ai-activity/');
  });

  test('offers revoke only where the server gave a revoke id', async () => {
    renderFeed();
    const own = await screen.findByTestId('ai-activity-3');
    expect(within(own).getByRole('button', { name: /revoke/i })).toBeInTheDocument();
    expect(within(screen.getByTestId('ai-activity-2')).queryByRole('button', { name: /revoke/i })).toBeNull();
    expect(screen.getByTestId('ai-activity-2')).toHaveTextContent('Revoked');
    expect(within(screen.getByTestId('ai-activity-1')).queryByRole('button', { name: /revoke/i })).toBeNull();
  });

  test('revoking asks first, then revokes the token and reloads the feed', async () => {
    const u = userEvent.setup();
    vi.mocked(api.delete).mockResolvedValue({ data: null } as never);
    renderFeed();
    await u.click(within(await screen.findByTestId('ai-activity-3')).getByRole('button', { name: /revoke/i }));
    await u.click(within(await screen.findByTestId('confirm-modal')).getByRole('button', { name: /^revoke$/i }));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/ai-activity/tokens/hash-live'));
    await waitFor(() => expect(vi.mocked(api.get).mock.calls.length).toBeGreaterThan(1));
  });

  test('an org owner or admin is offered revoke on a teammate\'s token and told whose it is', async () => {
    const u = userEvent.setup();
    vi.mocked(api.delete).mockResolvedValue({ data: null } as never);
    vi.mocked(api.get).mockResolvedValue({
      data: { calls: [{ id: 9, tool: 'execute_sql', status: 'ok', tokenName: 'Their MCP', userId: 'teammate', at: '2026-10-06T00:58:00Z', mine: false, tokenId: 'hash-theirs' }] },
    } as never);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><AiActivityFeed projectId="proj-1" /></QueryClientProvider>);
    await u.click(within(await screen.findByTestId('ai-activity-9')).getByRole('button', { name: /revoke/i }));
    const modal = await screen.findByTestId('confirm-modal');
    expect(modal).toHaveTextContent(/another member's token/i);
    await u.click(within(modal).getByRole('button', { name: /^revoke$/i }));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/ai-activity/tokens/hash-theirs'));
  });

  test('an empty project says so', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { calls: [] } } as never);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><AiActivityFeed projectId="proj-1" /></QueryClientProvider>);
    expect(await screen.findByText(/no ai tool has called this project yet/i)).toBeInTheDocument();
  });

  test('a refused revoke is shown', async () => {
    const u = userEvent.setup();
    vi.mocked(api.delete).mockRejectedValue({ response: { data: { error: 'token not found' } } });
    renderFeed();
    await u.click(within(await screen.findByTestId('ai-activity-3')).getByRole('button', { name: /revoke/i }));
    await u.click(within(await screen.findByTestId('confirm-modal')).getByRole('button', { name: /^revoke$/i }));
    expect(await screen.findByText('token not found')).toBeInTheDocument();
  });

  test('a feed that cannot load says so', async () => {
    vi.mocked(api.get).mockRejectedValue({ response: { data: { error: 'insufficient project role' } } });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><AiActivityFeed projectId="proj-1" /></QueryClientProvider>);
    expect(await screen.findByText('insufficient project role')).toBeInTheDocument();
  });
});

describe('call result', () => {
  const base = { id: 1, tool: 't', tokenName: 'n', userId: 'u', at: '2026-10-06T00:00:00Z', mine: false };
  test('names each outcome', () => {
    expect(callResult({ ...base, status: 'ok' })).toBe('OK');
    expect(callResult({ ...base, status: 'error', httpStatus: 404 })).toBe('Refused (404)');
    expect(callResult({ ...base, status: 'error', httpStatus: 409 })).toBe('Failed (409)');
    expect(callResult({ ...base, status: 'error' })).toBe('Failed');
  });
});
