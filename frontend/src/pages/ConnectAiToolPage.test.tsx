import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ConnectAiToolPage } from './ConnectAiToolPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

function renderPage() {
  vi.mocked(api.get).mockResolvedValue({ data: [] } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/ai-tools']}>
        <Routes>
          <Route path="/project/:projectId/ai-tools" element={<ConnectAiToolPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function created(name: string, scopes: string) {
  return { data: { token: 'excb_full_secret', prefix: 'excb_ful', name, scopes, projectId: 'proj-1' } } as never;
}

describe('Connect your AI tool', () => {
  beforeEach(() => vi.clearAllMocks());

  test('a read-only Codex connection mints a read token bound to the project and shows its config once', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue(created('Codex MCP', 'read'));
    renderPage();

    await u.click(screen.getByRole('radio', { name: 'Codex' }));
    await u.click(screen.getByRole('radio', { name: /read only/i }));
    await u.click(screen.getByRole('button', { name: /create token/i }));

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/auth/tokens', {
      name: 'Codex MCP', scopes: ['read'], expiresIn: '90d', projectId: 'proj-1',
    }));
    const setup = await screen.findByTestId('mcp-setup');
    expect(setup).toHaveTextContent('export EXCALIBASE_TOKEN=excb_full_secret');
    expect(setup).toHaveTextContent('~/.codex/config.toml');
    expect(setup).toHaveTextContent(`${window.location.origin}/mcp?project=proj-1&read_only=true`);
    expect(setup).toHaveTextContent('bearer_token_env_var = "EXCALIBASE_TOKEN"');

    await u.click(within(setup).getByRole('button', { name: /i have saved it/i }));
    expect(screen.queryByText(/excb_full_secret/)).not.toBeInTheDocument();
  });

  test('a read-write Claude Code connection asks for write and leaves read_only off', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue(created('Claude Code MCP', 'read,write'));
    renderPage();

    await u.click(screen.getByRole('radio', { name: 'Claude Code' }));
    await u.click(screen.getByRole('radio', { name: /read and write/i }));
    await u.click(screen.getByRole('button', { name: /create token/i }));

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/auth/tokens', expect.objectContaining({
      name: 'Claude Code MCP', scopes: ['read', 'write'], projectId: 'proj-1',
    })));
    const setup = await screen.findByTestId('mcp-setup');
    expect(setup).toHaveTextContent(`claude mcp add --transport http excalibase "${window.location.origin}/mcp?project=proj-1"`);
    expect(setup).not.toHaveTextContent('read_only');
  });

  test('read only is the default', () => {
    renderPage();
    expect(screen.getByRole('radio', { name: /read only/i })).toBeChecked();
  });

  test('a refusal from the token API is shown', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({ response: { data: { error: 'rate limit exceeded' } } });
    renderPage();
    await u.click(screen.getByRole('button', { name: /create token/i }));
    expect(await screen.findByText('rate limit exceeded')).toBeInTheDocument();
    expect(screen.queryByTestId('mcp-setup')).not.toBeInTheDocument();
  });
});
