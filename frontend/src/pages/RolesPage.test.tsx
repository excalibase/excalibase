import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { Toaster } from 'sonner';
import { RolesPage } from './RolesPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

const REJECTION = {
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: 'role "reporting" already exists', status: 409 } },
};

const ROLES = [
  { name: 'reporting', login: true, superuser: false, createDb: false, createRole: false, connLimit: -1 },
  { name: 'postgres', login: true, superuser: true, createDb: true, createRole: true, connLimit: 5 },
];

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Toaster />
      <MemoryRouter initialEntries={['/project/proj-1/roles']}>
        <Routes>
          <Route path="/project/:projectId/roles" element={<RolesPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function openCreatePanel() {
  fireEvent.click(await screen.findByTestId('create-role-btn'));
}

describe('RolesPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: ROLES } as never);
  });

  test('lists roles and offers no drop for a superuser', async () => {
    renderPage();
    expect(await screen.findByTestId('role-row-reporting')).toHaveTextContent('unlimited');
    expect(screen.getByTestId('role-row-postgres')).toHaveTextContent('YES');
    expect(screen.queryByTestId('drop-role-postgres')).not.toBeInTheDocument();
  });

  test('an empty or blank name cannot be submitted', async () => {
    renderPage();
    await openCreatePanel();
    const submit = screen.getByTestId('create-role-submit');
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByTestId('role-name-input'), { target: { value: '   ' } });
    expect(submit).toBeDisabled();
    fireEvent.click(submit);
    expect(api.post).not.toHaveBeenCalled();
  });

  test('creates a role with its password and login flag', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    renderPage();
    await openCreatePanel();
    fireEvent.change(screen.getByTestId('role-name-input'), { target: { value: '  analyst \n' } });
    fireEvent.change(screen.getByTestId('role-password-input'), { target: { value: 's3cret' } });
    fireEvent.click(screen.getByLabelText(/Can Login/));
    fireEvent.click(screen.getByTestId('create-role-submit'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/schema/proj-1/roles', { name: 'analyst', password: 's3cret', login: false }),
    );
    await waitFor(() => expect(screen.queryByTestId('role-name-input')).not.toBeInTheDocument());
  });

  test('a refused create shows the server reason, not the status code', async () => {
    vi.mocked(api.post).mockRejectedValue(REJECTION);
    renderPage();
    await openCreatePanel();
    fireEvent.change(screen.getByTestId('role-name-input'), { target: { value: 'reporting' } });
    fireEvent.click(screen.getByTestId('create-role-submit'));
    expect(await screen.findByText('role "reporting" already exists')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    expect(screen.getByTestId('role-name-input')).toHaveValue('reporting');
  });

  test('a double click on create sends one request', async () => {
    vi.mocked(api.post).mockReturnValue(new Promise(() => {}) as never);
    renderPage();
    await openCreatePanel();
    fireEvent.change(screen.getByTestId('role-name-input'), { target: { value: 'analyst' } });
    const submit = screen.getByTestId('create-role-submit');
    fireEvent.click(submit);
    fireEvent.click(submit);
    await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1));
    expect(api.post).toHaveBeenCalledTimes(1);
  });

  test('a refused drop shows the server reason', async () => {
    vi.mocked(api.delete).mockRejectedValue({
      ...REJECTION,
      response: { status: 409, data: { error: 'role "reporting" owns objects', status: 409 } },
    });
    renderPage();
    fireEvent.click(await screen.findByTestId('drop-role-reporting'));
    fireEvent.click(screen.getByTestId('modal-confirm'));
    expect(await screen.findByText('role "reporting" owns objects')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a double click on drop sends one request', async () => {
    vi.mocked(api.delete).mockReturnValue(new Promise(() => {}) as never);
    renderPage();
    fireEvent.click(await screen.findByTestId('drop-role-reporting'));
    const confirm = screen.getByTestId('modal-confirm');
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    await waitFor(() => expect(api.delete).toHaveBeenCalledTimes(1));
    expect(api.delete).toHaveBeenCalledTimes(1);
  });
});
