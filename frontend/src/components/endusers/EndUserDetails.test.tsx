import { describe, test, expect, beforeEach, vi } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { EndUserRoles } from './EndUserRoles';
import { EndUserDetails } from './EndUserDetails';
import { api } from '../../api/client';
import { useAuthStore } from '../../stores/auth-store';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const USERS = { users: [{ id: 3, email: 'alice@x.test', role: 'user', allowedRoles: ['user'] }], total: 1 };
const ALICE = {
  id: 3,
  email: 'alice@x.test',
  fullName: 'Alice Liddell',
  role: 'user',
  allowedRoles: ['user'],
  userMetadata: { theme: 'dark' },
  appMetadata: { plan: 'free', trial: true },
};

function renderPanel(authuser: boolean) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features: { authuser } } } as never);
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

async function openDetails() {
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Details of alice@x.test' }));
  return { user, panel: await screen.findByTestId('end-user-details-3') };
}

function setAppMetadata(text: string) {
  fireEvent.change(screen.getByLabelText('App metadata of alice@x.test'), { target: { value: text } });
}

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.getState().setAuth({ id: 'u1', username: 'adm', email: 'a@x.test', role: 'user' });
});

describe('EndUserDetails', () => {
  test('nothing new shows while authuser is off', async () => {
    renderPanel(false);
    await screen.findByTestId('end-user-3');
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByRole('button', { name: 'Details of alice@x.test' })).not.toBeInTheDocument();
    expect(screen.queryByText(/delete user/i)).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith('/projects/p1/end-users/3');
  });

  test('shows the name, email and both metadata objects', async () => {
    renderPanel(true);
    const { panel } = await openDetails();
    expect(await within(panel).findByText('Alice Liddell')).toBeInTheDocument();
    expect(panel).toHaveTextContent('alice@x.test');
    expect(within(panel).getByTestId('user-metadata')).toHaveTextContent('"theme": "dark"');
    expect(screen.getByLabelText('App metadata of alice@x.test')).toHaveValue(JSON.stringify(ALICE.appMetadata, null, 2));
  });

  test('saves app metadata as a merge patch', async () => {
    vi.mocked(api.patch).mockResolvedValue({ data: { ...ALICE, appMetadata: { plan: 'pro', seats: 3 } } } as never);
    renderPanel(true);
    const { user, panel } = await openDetails();
    await within(panel).findByText('Alice Liddell');
    setAppMetadata('{"plan":"pro","seats":3}');
    await user.click(within(panel).getByRole('button', { name: 'Save app metadata' }));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/projects/p1/end-users/3', { appMetadata: { plan: 'pro', seats: 3, trial: null } }),
    );
  });

  test.each([
    ['[1]', /must be a JSON object/],
    ['{plan', /not valid JSON/],
  ])('refuses %s before sending', async (text, message) => {
    renderPanel(true);
    const { user, panel } = await openDetails();
    await within(panel).findByText('Alice Liddell');
    setAppMetadata(text);
    await user.click(within(panel).getByRole('button', { name: 'Save app metadata' }));
    expect(within(panel).getByRole('alert')).toHaveTextContent(message);
    expect(api.patch).not.toHaveBeenCalled();
  });

  test('an unchanged box sends nothing', async () => {
    renderPanel(true);
    const { user, panel } = await openDetails();
    await within(panel).findByText('Alice Liddell');
    await user.click(within(panel).getByRole('button', { name: 'Save app metadata' }));
    expect(within(panel).getByRole('alert')).toHaveTextContent(/nothing changed/i);
    expect(api.patch).not.toHaveBeenCalled();
  });

  test('deleting needs the user email typed', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: undefined } as never);
    renderPanel(true);
    const { user, panel } = await openDetails();
    await within(panel).findByText('Alice Liddell');
    await user.click(within(panel).getByRole('button', { name: 'Delete user' }));
    const confirm = screen.getByTestId('modal-confirm');
    expect(confirm).toBeDisabled();
    await user.type(screen.getByTestId('confirm-input'), 'alice@x.tes');
    expect(confirm).toBeDisabled();
    await user.type(screen.getByTestId('confirm-input'), 't');
    expect(confirm).toBeEnabled();
    await user.click(confirm);
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/projects/p1/end-users/3'));
  });

  test('shows auth refusing the delete', async () => {
    vi.mocked(api.delete).mockRejectedValue({
      response: { status: 409, data: { error: 'an app table still references the account', code: 'user_referenced' } },
    });
    renderPanel(true);
    const { user, panel } = await openDetails();
    await within(panel).findByText('Alice Liddell');
    await user.click(within(panel).getByRole('button', { name: 'Delete user' }));
    await user.type(screen.getByTestId('confirm-input'), 'alice@x.test');
    await user.click(screen.getByTestId('modal-confirm'));
    expect(await within(panel).findByRole('alert')).toHaveTextContent('an app table still references the account');
  });

  test('says when auth cannot load the user', async () => {
    const refused = Promise.reject({ response: { status: 404, data: { error: 'no such user', code: 'user_not_found' } } });
    refused.catch(() => undefined);
    vi.mocked(api.get).mockReturnValue(refused as never);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <EndUserDetails projectId="p1" user={USERS.users[0]} onDeleted={() => undefined} />
      </QueryClientProvider>,
    );
    expect(await screen.findByRole('alert')).toHaveTextContent('no such user');
    expect(api.get).toHaveBeenCalledWith('/projects/p1/end-users/3');
  });
});
