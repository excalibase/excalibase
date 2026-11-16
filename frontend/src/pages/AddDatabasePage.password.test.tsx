import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AddDatabasePage } from './AddDatabasePage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));
const navigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => navigate };
});

const SHOWN = 'Added-Once-Pass-91zK';
const CATALOG = { documentDbRef: 'x', majors: [{ major: '16', available: true, documentDb: true }] };
const PROJECT = { projectId: 'proj-1', status: 'ACTIVE', noDatabase: true, canAddDatabase: true };

function renderPage() {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/config') return { data: { deploymentMode: 'cloud', features: { onetimecredentials: true } } } as never;
    if (url === '/provision/proj-1') return { data: PROJECT } as never;
    if (url === '/postgres/catalog') return { data: CATALOG } as never;
    if (url === '/provision/proj-1/credentials') {
      return { data: { projectId: 'proj-1', host: 'h', port: 5432, databaseName: 'appdb', username: 'app', sslMode: 'require', connectionUrl: 'x' } } as never;
    }
    throw new Error(`unexpected GET ${url}`);
  });
  vi.mocked(api.post).mockResolvedValue({ data: { projectId: 'proj-1', status: 'ACTIVE', noDatabase: false, password: SHOWN } } as never);
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <MemoryRouter initialEntries={['/project/proj-1/database/add']}>
        <Routes><Route path="/project/:projectId/database/add" element={<AddDatabasePage />} /></Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('AddDatabasePage - one-time password', () => {
  beforeEach(() => { vi.clearAllMocks(); });

  test('refuses a short password before sending', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.type(await screen.findByLabelText(/database password/i), 'short');
    await user.click(await screen.findByTestId('pg-version-16'));
    await user.click(screen.getByTestId('add-database-submit'));
    expect(await screen.findByTestId('password-error')).toHaveTextContent(/at least 16/i);
    expect(api.post).not.toHaveBeenCalled();
  });

  test('shows the issued password once, then goes to the project when saved', async () => {
    const user = userEvent.setup();
    const { container } = renderPage();
    await screen.findByLabelText(/database password/i);
    await user.click(await screen.findByTestId('pg-version-16'));
    await user.click(screen.getByTestId('add-database-submit'));
    expect(await screen.findByTestId('otp-password')).toHaveTextContent(SHOWN);
    expect(navigate).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: /i've saved it/i }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith('/project/proj-1'));
    expect(container.innerHTML).not.toContain(SHOWN);
  });
});
