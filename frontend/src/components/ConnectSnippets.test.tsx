import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { ConnectSnippets } from './ConnectSnippets';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

function renderWith(config: Record<string, unknown>, tables: { name: string }[] = []) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/config') return { data: config } as never;
    if (url.startsWith('/schema/proj-1/tables')) return { data: tables } as never;
    return { data: {} } as never;
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <ConnectSnippets projectId="proj-1" />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('ConnectSnippets', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows the endpoints and an SDK snippet built from the server-reported API URL', async () => {
    renderWith({ deploymentMode: 'cloud', apiUrl: 'https://api.example.test' }, [{ name: 'todos' }]);
    expect(await screen.findByText('https://api.example.test/proj-1/graphql')).toBeInTheDocument();
    expect(screen.getByText('https://api.example.test/proj-1/api/v1')).toBeInTheDocument();
    const code = screen.getByTestId('connect-code');
    expect(code.textContent).toContain("from '@excalibase/sdk'");
    expect(code.textContent).toContain("url: 'https://api.example.test'");
    expect(code.textContent).toContain("db.rest.get('/todos?limit=10')");
    expect(screen.getByRole('link', { name: /api keys/i })).toHaveAttribute('href', '/project/proj-1/api-keys');
  });

  test('switches to the curl tab', async () => {
    renderWith({ deploymentMode: 'cloud', apiUrl: 'https://api.example.test' });
    await userEvent.click(await screen.findByRole('tab', { name: /curl/i }));
    expect(screen.getByTestId('connect-code').textContent).toContain('grant_type');
  });

  test('a server that reports no API URL gets an explanation, not a guessed address', async () => {
    renderWith({ deploymentMode: 'selfhosted' });
    expect(await screen.findByTestId('connect-unavailable')).toHaveTextContent(/PUBLIC_BASE_URL/);
    expect(screen.queryByTestId('connect-code')).toBeNull();
    expect(screen.queryByText(/excalibase\.io/)).toBeNull();
  });
});
