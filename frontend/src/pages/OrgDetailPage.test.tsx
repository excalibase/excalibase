import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { OrgDetailPage } from './OrgDetailPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const org = { id: 'o1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: 'u-owner' };
const members = [
  { orgId: 'o1', userId: 'u-owner', role: 'owner', email: 'owner@x.test', username: 'owner' },
  { orgId: 'o1', userId: 'u-dev', role: 'developer', email: 'dev@x.test', username: 'dev' },
];
const invites = [{ id: 1, orgId: 'o1', email: 'waiting@x.test', role: 'viewer', invitedBy: 'u-owner' }];
const projects = [
  { projectId: 'p-mine', orgId: 'o1', status: 'READY', tier: 'FREE' },
  { projectId: 'p-other', orgId: 'o2', status: 'READY', tier: 'FREE' },
];

function mockReads() {
  vi.mocked(api.get).mockImplementation((url: string) => {
    const bodies: Record<string, unknown> = {
      '/orgs/o1': org,
      '/orgs/o1/members': members,
      '/orgs/o1/invites': invites,
      '/provision': projects,
    };
    if (url in bodies) return Promise.resolve({ data: bodies[url] } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/orgs/o1']}>
      <Routes>
        <Route path="/orgs/:orgId" element={<OrgDetailPage />} />
        <Route path="/orgs" element={<div data-testid="orgs-list">ORGS</div>} />
        <Route path="/provision" element={<div data-testid="provision">PROVISION</div>} />
        <Route path="/project/:projectId" element={<div data-testid="project">PROJECT</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

async function openMembers(u: ReturnType<typeof userEvent.setup>) {
  renderPage();
  await u.click(await screen.findByRole('button', { name: /members/i }));
}

async function invite(u: ReturnType<typeof userEvent.setup>, email: string) {
  await u.click(screen.getByRole('button', { name: /invite member/i }));
  await u.type(screen.getByPlaceholderText('user@example.com'), email);
  await u.click(screen.getByRole('button', { name: /^invite$/i }));
}

describe('OrgDetailPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.getState().setAuth({ id: 'u-owner', username: 'owner', email: 'owner@x.test', role: 'user' });
    mockReads();
  });

  test('lists only this org\'s projects and opens one', async () => {
    const u = userEvent.setup();
    renderPage();
    expect(await screen.findByText('p-mine')).toBeInTheDocument();
    expect(screen.queryByText('p-other')).not.toBeInTheDocument();
    await u.click(screen.getByText('p-mine'));
    expect(await screen.findByTestId('project')).toBeInTheDocument();
  });

  test('inviting an address with no account shows its one-time link', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'pending', inviteLink: '/register?invite=tok' } } as never);
    await openMembers(u);
    await invite(u, 'new@x.test');

    expect(await screen.findByTestId('invite-link')).toHaveValue(`${window.location.origin}/register?invite=tok`);
    expect(screen.getByTestId('invite-link-notice')).toHaveTextContent('new@x.test');
    expect(api.post).toHaveBeenCalledWith('/orgs/o1/members', { email: 'new@x.test', role: 'developer' });
  });

  test('inviting an existing account shows no link', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'invited' } } as never);
    await openMembers(u);
    await invite(u, 'dev2@x.test');

    await waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(screen.queryByTestId('invite-link-notice')).not.toBeInTheDocument();
  });

  test('a refused invite is reported', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({ response: { data: { error: 'failed to create invite' } } });
    await openMembers(u);
    await invite(u, 'new@x.test');

    expect(await screen.findByText('failed to create invite')).toBeInTheDocument();
  });

  test('pending invites are listed and members can be managed', async () => {
    const u = userEvent.setup();
    vi.mocked(api.patch).mockResolvedValue({ data: null } as never);
    vi.mocked(api.delete).mockResolvedValue({ data: null } as never);
    await openMembers(u);

    expect(screen.getByText('waiting@x.test')).toBeInTheDocument();
    const devRow = screen.getByText('dev@x.test').closest('div.flex') as HTMLElement;
    await u.selectOptions(within(devRow.parentElement as HTMLElement).getByRole('combobox'), 'viewer');
    expect(api.patch).toHaveBeenCalledWith('/orgs/o1/members/u-dev', { role: 'viewer' });

    await u.click(screen.getByTitle('Remove member'));
    expect(api.delete).toHaveBeenCalledWith('/orgs/o1/members/u-dev');
  });

  test('settings show the plan read-only to the org owner and delete the org', async () => {
    const u = userEvent.setup();
    vi.mocked(api.delete).mockResolvedValue({ data: null } as never);
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    renderPage();
    await u.click(await screen.findByRole('button', { name: /settings/i }));

    expect(screen.queryByLabelText('Tier')).not.toBeInTheDocument();
    expect(screen.getByTestId('org-plan')).toHaveTextContent('FREE');

    await u.click(screen.getByRole('button', { name: /delete/i }));
    expect(await screen.findByTestId('orgs-list')).toBeInTheDocument();
  });

  test('a platform admin changes the plan', async () => {
    const u = userEvent.setup();
    useAuthStore.getState().setAuth({ id: 'u-owner', username: 'owner', email: 'owner@x.test', role: 'platform_admin' });
    vi.mocked(api.patch).mockResolvedValue({ data: { ...org, tier: 'STANDARD' } } as never);
    renderPage();
    await u.click(await screen.findByRole('button', { name: /settings/i }));

    await u.selectOptions(screen.getByLabelText('Tier'), 'STANDARD');
    expect(api.patch).toHaveBeenCalledWith('/orgs/o1', { tier: 'STANDARD' });
  });
});
