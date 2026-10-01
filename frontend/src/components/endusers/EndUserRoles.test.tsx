import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { EndUserRoles } from './EndUserRoles';
import { api } from '../../api/client';
import { useAuthStore } from '../../stores/auth-store';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn() },
}));

const USERS = {
  users: [
    { id: 3, email: 'alice@x.test', role: 'user', allowedRoles: ['user'] },
    { id: 4, email: 'bob@x.test', role: 'editor', allowedRoles: ['editor', 'user'] },
  ],
  total: 2,
};

function renderPanel(orgRole = 'admin', listing: Promise<unknown> = Promise.resolve({ data: USERS })) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p1') return Promise.resolve({ data: { projectId: 'p1', orgId: 'o1' } } as never);
    if (url === '/orgs') return Promise.resolve({ data: [{ id: 'o1', name: 'O', slug: 'o', role: orgRole }] } as never);
    if (url === '/projects/p1/end-users/') return listing as never;
    return Promise.reject(new Error('unexpected ' + url));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <EndUserRoles projectId="p1" />
    </QueryClientProvider>,
  );
}

const row = (id: number) => screen.findByTestId(`end-user-${id}`);

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.getState().setAuth({ id: 'u1', username: 'adm', email: 'a@x.test', role: 'user' });
});

describe('EndUserRoles', () => {
  test('lists end users with their roles and warns that a change signs them out', async () => {
    renderPanel();
    const bob = await row(4);
    expect(bob).toHaveTextContent('bob@x.test');
    expect(bob).toHaveTextContent('editor');
    expect(bob).toHaveTextContent('editor, user');
    expect(screen.getByText(/signs the user out of their existing sessions/i)).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith('/projects/p1/end-users/', { params: { limit: 100 } });
  });

  test('a developer is told only admins can manage roles, and nothing is fetched', async () => {
    renderPanel('developer');
    expect(await screen.findByText(/only project admins and owners/i)).toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith('/projects/p1/end-users/', expect.anything());
  });

  test('sets a role and allowed roles', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: { id: 3, email: 'alice@x.test', role: 'editor', allowedRoles: ['editor', 'user'] } } as never);
    renderPanel();
    const user = userEvent.setup();
    const alice = await row(3);
    await user.click(within(alice).getByRole('button', { name: 'Change role of alice@x.test' }));
    const role = within(alice).getByLabelText('Role for alice@x.test');
    await user.clear(role);
    await user.type(role, 'editor');
    const allowed = within(alice).getByLabelText('Allowed roles for alice@x.test');
    await user.clear(allowed);
    await user.type(allowed, 'editor, user');
    await user.click(within(alice).getByRole('button', { name: 'Save' }));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/projects/p1/end-users/3/role', { role: 'editor', allowedRoles: ['editor', 'user'] }),
    );
  });

  test('empty allowed roles sends only the role', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: {} } as never);
    renderPanel();
    const user = userEvent.setup();
    const alice = await row(3);
    await user.click(within(alice).getByRole('button', { name: 'Change role of alice@x.test' }));
    await user.clear(within(alice).getByLabelText('Allowed roles for alice@x.test'));
    await user.click(within(alice).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith('/projects/p1/end-users/3/role', { role: 'user' }));
  });

  test.each([
    ['anon', '', /reserved/],
    ['service', '', /reserved/],
    ['pg_admin', '', /reserved/],
    ['excalibase_x', '', /reserved/],
    ['Editor', '', /lower-case letters/],
    ['editor', 'user', /must include editor/],
    ['editor', 'editor, editor', /listed twice/],
    ['editor', 'editor, anon', /reserved/],
  ])('refuses role %s with allowed "%s" before sending', async (roleName, allowedRoles, message) => {
    renderPanel();
    const user = userEvent.setup();
    const alice = await row(3);
    await user.click(within(alice).getByRole('button', { name: 'Change role of alice@x.test' }));
    const role = within(alice).getByLabelText('Role for alice@x.test');
    await user.clear(role);
    await user.type(role, roleName);
    const allowed = within(alice).getByLabelText('Allowed roles for alice@x.test');
    await user.clear(allowed);
    if (allowedRoles) await user.type(allowed, allowedRoles);
    await user.click(within(alice).getByRole('button', { name: 'Save' }));
    expect(within(alice).getByRole('alert')).toHaveTextContent(message);
    expect(api.put).not.toHaveBeenCalled();
  });

  test('shows the server refusal', async () => {
    vi.mocked(api.put).mockRejectedValue({ response: { status: 400, data: { error: 'role editor is not defined', code: 'invalid_role' } } });
    renderPanel();
    const user = userEvent.setup();
    const alice = await row(3);
    await user.click(within(alice).getByRole('button', { name: 'Change role of alice@x.test' }));
    await user.click(within(alice).getByRole('button', { name: 'Save' }));
    expect(await within(alice).findByRole('alert')).toHaveTextContent('role editor is not defined');
  });

  test('cancel closes the editor', async () => {
    renderPanel();
    const user = userEvent.setup();
    const alice = await row(3);
    await user.click(within(alice).getByRole('button', { name: 'Change role of alice@x.test' }));
    await user.click(within(alice).getByRole('button', { name: 'Cancel' }));
    expect(within(alice).queryByLabelText('Role for alice@x.test')).not.toBeInTheDocument();
  });

  test('a listing failure is shown', async () => {
    const refused = Promise.reject({ response: { status: 503, data: { error: 'end-user management is unavailable' } } });
    refused.catch(() => undefined);
    renderPanel('owner', refused);
    expect(await screen.findByRole('alert')).toHaveTextContent('end-user management is unavailable');
  });

  test('says when there are no end users', async () => {
    renderPanel('owner', Promise.resolve({ data: { users: [] } }));
    expect(await screen.findByText(/no end users yet/i)).toBeInTheDocument();
  });
});
