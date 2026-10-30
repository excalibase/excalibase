import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { EndUserRoles } from './EndUserRoles';
import { api } from '../../api/client';
import { useAuthStore } from '../../stores/auth-store';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const USERS = { users: [{ id: 3, email: 'alice@x.test', role: 'user', allowedRoles: ['user'] }], total: 1 };
const ALICE = { id: 3, email: 'alice@x.test', role: 'user', allowedRoles: ['user'], userMetadata: {}, appMetadata: {} };

function renderPanel(features: Record<string, boolean>) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features } } as never);
    if (url === '/provision/p1') return Promise.resolve({ data: { projectId: 'p1', orgId: 'o1' } } as never);
    if (url === '/orgs') return Promise.resolve({ data: [{ id: 'o1', name: 'O', slug: 'o', role: 'admin' }] } as never);
    if (url === '/projects/p1/end-users/') return Promise.resolve({ data: USERS } as never);
    if (url === '/projects/p1/end-users/3') return Promise.resolve({ data: ALICE } as never);
    return Promise.reject(new Error('unexpected ' + url));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <EndUserRoles projectId="p1" />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.getState().setAuth({ id: 'u1', username: 'adm', email: 'a@x.test', role: 'user' });
});

describe('Reset MFA', () => {
  test('is not offered while authmfa is off', async () => {
    renderPanel({ authuser: true });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Details of alice@x.test' }));
    await within(await screen.findByTestId('end-user-details-3')).findByText('alice@x.test');
    expect(screen.queryByRole('button', { name: /reset mfa/i })).not.toBeInTheDocument();
  });

  test('sits in the details with authuser, and resets after a confirm', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: undefined } as never);
    renderPanel({ authuser: true, authmfa: true });
    const user = userEvent.setup();
    expect(await screen.findByTestId('end-user-3')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reset MFA of alice@x.test' })).not.toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: 'Details of alice@x.test' }));
    const panel = await screen.findByTestId('end-user-details-3');
    await user.click(await within(panel).findByRole('button', { name: 'Reset MFA of alice@x.test' }));
    expect(screen.getByTestId('confirm-modal')).toHaveTextContent(/signed out/i);
    expect(api.delete).not.toHaveBeenCalled();
    await user.click(screen.getByTestId('modal-confirm'));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/projects/p1/end-users/3/factors'));
  });

  test('sits in the row when authmfa is on without authuser', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: undefined } as never);
    renderPanel({ authmfa: true });
    const user = userEvent.setup();
    const row = await screen.findByTestId('end-user-3');
    await user.click(await within(row).findByRole('button', { name: 'Reset MFA of alice@x.test' }));
    await user.click(screen.getByTestId('modal-confirm'));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/projects/p1/end-users/3/factors'));
  });

  test("shows auth's refusal", async () => {
    vi.mocked(api.delete).mockRejectedValue({ response: { status: 404, data: { error: 'no such user', code: 'user_not_found' } } });
    renderPanel({ authmfa: true });
    const user = userEvent.setup();
    const row = await screen.findByTestId('end-user-3');
    await user.click(await within(row).findByRole('button', { name: 'Reset MFA of alice@x.test' }));
    await user.click(screen.getByTestId('modal-confirm'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/no such user/);
  });
});
