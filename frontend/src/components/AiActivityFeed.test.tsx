import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AiActivityFeed } from './AiActivityFeed';
import { api } from '../api/client';

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

  test('offers revoke only for the caller\'s own live token', async () => {
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
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/auth/tokens/hash-live'));
    await waitFor(() => expect(vi.mocked(api.get).mock.calls.length).toBeGreaterThan(1));
  });

  test('an empty project says so', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { calls: [] } } as never);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><AiActivityFeed projectId="proj-1" /></QueryClientProvider>);
    expect(await screen.findByText(/no ai tool has called this project yet/i)).toBeInTheDocument();
  });
});
