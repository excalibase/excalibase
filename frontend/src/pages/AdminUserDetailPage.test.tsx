import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { api } from '../api/client';
import { AdminUserDetailPage } from './AdminUserDetailPage';
import { useAuthStore } from '../stores/auth-store';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const WHEN = '2026-11-27T10:00:00Z';
const detail = (over: Record<string, unknown> = {}) => ({
  profile: { id: 'u1', email: 'cust@x.test', username: 'cust', role: 'user', status: 'active', emailVerified: true, emailVerifiedAt: WHEN, signedUpAt: WHEN, lastLoginAt: WHEN, lastLoginIp: '203.0.113.9', tempMail: true,
    trust: { state: 'limited', method: 'github', reason: 'account too young', decidedAt: WHEN } },
  suspension: { suspended: false },
  suspensions: [{ action: 'user.suspend', actorEmail: 'boss@x.test', reason: 'proxy node', at: WHEN }, { action: 'user.unsuspend', actorEmail: 'boss@x.test', reason: 'appeal accepted', at: WHEN }],
  trustHistory: [{ to: 'limited', method: 'github', reason: 'account too young', actor: 'system', at: WHEN }],
  orgs: [{ id: 'o1', name: 'Cust Co', slug: 'cust-co', tier: 'FREE', role: 'owner', owner: true }, { id: 'o2', name: 'Other Co', slug: 'other-co', tier: 'STANDARD', role: 'developer', owner: false }],
  projects: [{ projectId: 'p1', name: 'shop', orgId: 'o1', orgName: 'Cust Co', status: 'ACTIVE' }],
  signIn: [{ kind: 'provider', provider: 'github', email: 'cust@x.test', linkedAt: WHEN }],
  audit: [{ id: '5', at: WHEN, actorEmail: 'boss@x.test', action: 'user.suspend', resource: 'user', resourceId: 'u1', details: 'proxy node' }],
  ...over,
});

function renderPage(data: unknown, userId = 'u1', role = 'platform_admin') {
  useAuthStore.getState().setAuth({ id: 'boss', username: 'boss', email: 'boss@x.test', role } as never);
  if (data instanceof Error || (data as { response?: unknown }).response) vi.mocked(api.get).mockRejectedValue(data);
  else vi.mocked(api.get).mockResolvedValue({ data } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/admin/users/${userId}`]}>
        <Routes><Route path="/admin/users/:userId" element={<AdminUserDetailPage />} /><Route path="/" element={<div>home</div>} /></Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const section = (name: string) => screen.getByRole('region', { name });

describe('AdminUserDetailPage', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => useAuthStore.getState().clearAuth());

  test.each(['platform_operator', 'platform_viewer', 'user'])('%s is sent home and nothing is read', async (role) => {
    renderPage(detail(), 'u1', role);
    expect(await screen.findByText('home')).toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalled();
  });

  test('shows the profile, trust, orgs with roles, projects, sign-in methods and recent audit', async () => {
    renderPage(detail());
    expect(await screen.findByRole('heading', { name: 'cust@x.test' })).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith('/admin/accounts/u1');
    const profile = section('Profile');
    expect(within(profile).getByText('203.0.113.9')).toBeInTheDocument();
    expect(within(profile).getByText('Verified')).toBeInTheDocument();
    expect(within(profile).getByText('temp mail')).toBeInTheDocument();
    const trust = section('Trust');
    expect(within(trust).getByText('Limited')).toBeInTheDocument();
    expect(within(trust).getAllByText(/account too young/)).toHaveLength(2);
    const orgs = section('Organizations');
    expect(within(orgs).getByText('Cust Co')).toBeInTheDocument();
    expect(within(orgs).getByText('developer')).toBeInTheDocument();
    expect(within(section('Projects')).getByRole('link', { name: 'shop' })).toHaveAttribute('href', '/admin?tab=projects&project=p1');
    expect(within(section('Sign-in methods')).getByText(/github/i)).toBeInTheDocument();
    expect(within(section('Recent audit')).getByText('user.suspend')).toBeInTheDocument();
  });

  test('lists the suspension history with who and why', async () => {
    renderPage(detail());
    const history = within(await screen.findByRole('region', { name: 'Suspension' }));
    expect(history.getByText('Not suspended')).toBeInTheDocument();
    expect(history.getByText('proxy node')).toBeInTheDocument();
    expect(history.getByText('appeal accepted')).toBeInTheDocument();
  });

  test('suspending asks for a reason first and sends nothing until it is confirmed', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { userId: 'u1', status: 'suspended', appsStopped: [], appsRestored: [], appsFailed: [] } } as never);
    renderPage(detail());
    await userEvent.click(await screen.findByRole('button', { name: 'Suspend account' }));
    const dialog = screen.getByRole('dialog', { name: 'Suspend cust@x.test' });
    expect(api.post).not.toHaveBeenCalled();
    const confirm = within(dialog).getByRole('button', { name: 'Suspend' });
    expect(confirm).toBeDisabled();
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'Reason' }), 'abusing the free tier');
    await userEvent.click(confirm);
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/admin/console/users/u1/suspend', { reason: 'abusing the free tier', reasonCode: 'other' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(await screen.findByText(/Account is now suspended/)).toBeInTheDocument();
  });

  test('cancelling the confirmation changes nothing', async () => {
    renderPage(detail());
    await userEvent.click(await screen.findByRole('button', { name: 'Suspend account' }));
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(api.post).not.toHaveBeenCalled();
  });

  test('a suspended account offers Unsuspend with a reason and shows why it is suspended', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { userId: 'u1', status: 'active', appsStopped: [], appsRestored: [], appsFailed: [] } } as never);
    renderPage(detail({ profile: { ...detail().profile, status: 'suspended' }, suspension: { suspended: true, reason: 'proxy node', reasonCode: 'proxy', suspendedAt: WHEN } }));
    expect(await screen.findByRole('button', { name: 'Unsuspend account' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Suspend account' })).toBeNull();
    expect(within(section('Suspension')).getByText('Suspended')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Unsuspend account' }));
    await userEvent.type(screen.getByRole('textbox', { name: 'Reason' }), 'appeal accepted');
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Unsuspend' }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/admin/console/users/u1/unsuspend', { reason: 'appeal accepted' }));
  });

  test('a server refusal stays in the dialog in the server words', async () => {
    vi.mocked(api.post).mockRejectedValue({ response: { status: 409, data: { error: 'platform accounts cannot be suspended' } } });
    renderPage(detail());
    await userEvent.click(await screen.findByRole('button', { name: 'Suspend account' }));
    await userEvent.type(screen.getByRole('textbox', { name: 'Reason' }), 'long enough reason');
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Suspend' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('platform accounts cannot be suspended');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  test('a platform staff account has no suspend action', async () => {
    renderPage(detail({ profile: { ...detail().profile, role: 'platform_admin' } }));
    await screen.findByRole('heading', { name: 'cust@x.test' });
    expect(screen.queryByRole('button', { name: /suspend account/i })).toBeNull();
  });

  test('an account with no history says so in each section', async () => {
    renderPage(detail({ suspensions: [], trustHistory: [], orgs: [], projects: [], audit: [], profile: { ...detail().profile, trust: undefined, tempMail: undefined } }));
    await screen.findByRole('heading', { name: 'cust@x.test' });
    expect(screen.getByText('No organizations.')).toBeInTheDocument();
    expect(screen.getByText('No projects.')).toBeInTheDocument();
    expect(screen.getByText('No audit entries yet.')).toBeInTheDocument();
    expect(screen.getByText('No trust decision recorded.')).toBeInTheDocument();
    expect(screen.getByText('No suspensions on record.')).toBeInTheDocument();
  });

  test('an unknown account says so and links back to the console', async () => {
    renderPage({ response: { status: 404, data: { error: 'user not found' } } } as unknown as Error);
    expect(await screen.findByText('That account was not found.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Back to users/ })).toHaveAttribute('href', '/admin?tab=users');
  });
});
