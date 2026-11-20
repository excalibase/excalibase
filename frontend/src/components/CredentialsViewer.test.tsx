import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { api } from '../api/client';
import { CredentialsViewer } from './CredentialsViewer';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));
vi.mock('../hooks/useProjectRole', () => ({ useProjectRole: () => ({ canAdmin: true, canDevelop: true, role: 'admin', isLoading: false }) }));

const CREDENTIALS = {
  projectId: 'p-1', host: 'internal', port: 5432, databaseName: 'appdb', username: 'app', sslMode: 'require', connectionUrl: 'x',
};
const PUBLIC_ENDPOINT = {
  projectId: 'p-1', publicEnabled: true, available: true, host: 'p-1.db.excalibase.io', port: 26257, requireTls: true,
  database: 'appdb', username: 'app', connectionStrings: { requireTls: '', allowPlaintext: '' }, caCertificate: '',
};

function renderViewer() {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/provision/p-1/credentials') return { data: CREDENTIALS } as never;
    if (url === '/projects/p-1/db-endpoint') return { data: PUBLIC_ENDPOINT } as never;
    return { data: [] } as never;
  });
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter><CredentialsViewer projectId="p-1" /></MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('CredentialsViewer copy buttons', () => {
  beforeEach(() => vi.clearAllMocks());

  test('copies a row value and the connection string', async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
    renderViewer();
    await user.click(await screen.findByRole('button', { name: 'Copy username' }));
    expect(writeText).toHaveBeenCalledWith('app');
    await screen.findByText(/postgresql:\/\/app:<password>@p-1\.db\.excalibase\.io:26257\/appdb/);
    await user.click(screen.getAllByRole('button', { name: /^copy$/i })[0]);
    expect(writeText).toHaveBeenLastCalledWith(expect.stringContaining('p-1.db.excalibase.io:26257/appdb'));
    expect(await screen.findByText('Copied!')).toBeInTheDocument();
  });
});
