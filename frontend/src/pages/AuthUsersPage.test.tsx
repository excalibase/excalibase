import { describe, test, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AuthUsersPage } from './AuthUsersPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));
// Role management has its own tests; here it only has to stay out of the way.
vi.mock('../components/endusers/EndUserRoles', () => ({ EndUserRoles: () => null }));

const USERS = [
  {
    id: 1, email: 'ada@example.com', full_name: 'Ada Lovelace', role: 'user', enabled: true,
    created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', last_login_at: '2026-02-01T00:00:00Z',
  },
  {
    id: 2, email: 'bob@example.com', full_name: 'Bob Builder', role: 'admin', enabled: false,
    created_at: '2026-01-02T00:00:00Z', updated_at: '2026-01-02T00:00:00Z', last_login_at: null,
  },
];

const refusal = (reason: string) => ({
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: reason, status: 409 } },
});

const pending = () => new Promise<never>(() => {});

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/auth/users']}>
        <Routes>
          <Route path="/project/:projectId/auth/users" element={<AuthUsersPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function statusButton(userId: number) {
  return screen.getByTestId(`user-row-${userId}`).querySelectorAll('button')[0];
}

function deleteButton(userId: number) {
  return screen.getByTestId(`user-row-${userId}`).querySelectorAll('button')[1];
}

describe('AuthUsersPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: USERS } as never);
  });

  test('lists users and filters by email or name', async () => {
    renderPage();
    expect(await screen.findByTestId('user-row-1')).toHaveTextContent('Active');
    expect(screen.getByTestId('user-row-2')).toHaveTextContent('Never');

    fireEvent.change(screen.getByTestId('user-search'), { target: { value: 'builder' } });
    expect(screen.queryByTestId('user-row-1')).not.toBeInTheDocument();

    fireEvent.change(screen.getByTestId('user-search'), { target: { value: 'zzz' } });
    expect(screen.getByText('No users matching search')).toBeInTheDocument();
  });

  test('a refused enable/disable shows the server reason, not the status code', async () => {
    vi.mocked(api.patch).mockRejectedValue(refusal('cannot disable the last admin'));
    renderPage();
    await screen.findByTestId('user-row-1');
    fireEvent.click(statusButton(1));

    await waitFor(() => expect(api.patch).toHaveBeenCalledWith('/projects/proj-1/auth/users/1', { enabled: false }));
    const alert = await screen.findByTestId('auth-users-error');
    expect(alert).toHaveAttribute('role', 'alert');
    expect(alert).toHaveTextContent('cannot disable the last admin');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a double click on the status toggle sends one request', async () => {
    vi.mocked(api.patch).mockImplementation(pending);
    renderPage();
    await screen.findByTestId('user-row-1');
    const toggle = statusButton(1);
    fireEvent.click(toggle);
    fireEvent.click(toggle);

    await waitFor(() => expect(api.patch).toHaveBeenCalled());
    expect(api.patch).toHaveBeenCalledTimes(1);
  });

  test('delete needs the email typed, and a refusal says why', async () => {
    vi.mocked(api.delete).mockRejectedValue(refusal('user owns rows in orders'));
    renderPage();
    await screen.findByTestId('user-row-2');
    fireEvent.click(deleteButton(2));
    expect(screen.getByTestId('modal-confirm')).toBeDisabled();

    fireEvent.change(screen.getByTestId('confirm-input'), { target: { value: 'bob@example.com' } });
    fireEvent.click(screen.getByTestId('modal-confirm'));

    const alert = await screen.findByTestId('auth-users-error');
    expect(alert).toHaveTextContent('user owns rows in orders');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a double confirm deletes once and the dialog closes on success', async () => {
    vi.mocked(api.delete).mockResolvedValue({} as never);
    renderPage();
    await screen.findByTestId('user-row-2');
    fireEvent.click(deleteButton(2));
    fireEvent.change(screen.getByTestId('confirm-input'), { target: { value: 'bob@example.com' } });
    const confirm = screen.getByTestId('modal-confirm');
    fireEvent.click(confirm);
    fireEvent.click(confirm);

    await waitFor(() => expect(screen.queryByTestId('confirm-modal')).not.toBeInTheDocument());
    expect(api.delete).toHaveBeenCalledTimes(1);
    expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/auth/users/2');
  });

  test('an empty project says no users are registered', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [] } as never);
    renderPage();
    expect(await screen.findByText('No auth users registered yet')).toBeInTheDocument();
  });
});
