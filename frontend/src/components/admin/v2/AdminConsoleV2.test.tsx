import { beforeEach, describe, expect, test, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { api } from '../../../api/client';
import { AdminConsoleV2, adminTabs, initialTab } from './AdminConsoleV2';
import { ANALYTICS_TABS } from '../analytics/analyticsTabs';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn() } }));
vi.mock('../NeedsAttention', () => ({ NeedsAttention: () => <div>attention</div> }));
vi.mock('./ProjectsAdminTable', () => ({ ProjectsAdminTable: () => <div>projects list</div> }));
vi.mock('./UsersAdminTable', () => ({ UsersAdminTable: () => <div>users list</div> }));
vi.mock('./OrgsAdminTable', () => ({ OrgsAdminTable: () => <div>orgs list</div> }));
vi.mock('./AuditLogTab', () => ({ AuditLogTab: () => <div>audit log list</div> }));

describe('which tabs an admin sees', () => {
  test('a platform admin sees every section', () => {
    expect(adminTabs(true).map((t) => t.id)).toEqual(['attention', 'projects', 'users', 'orgs', 'audit', 'platform']);
  });

  test('an operator sees projects and the platform cards, never the people lists', () => {
    expect(adminTabs(false).map((t) => t.id)).toEqual(['projects', 'platform']);
  });

  test('the first analytics tab starts a new group', () => {
    expect(adminTabs(true, true).filter((t) => t.startsGroup).map((t) => t.id)).toEqual(['analytics-users']);
  });

  test('the analytics tabs sit before the platform cards and only for a platform admin', () => {
    expect(adminTabs(true, true).map((t) => t.id)).toEqual([
      'attention', 'projects', 'users', 'orgs', 'audit', 'analytics-users', ANALYTICS_TABS.abuse, 'analytics-traffic', 'analytics-storage', 'platform',
    ]);
    expect(adminTabs(false, true).map((t) => t.id)).toEqual(['projects', 'platform']);
  });

  test('the badges carry the counts once they are known and say what is missing before', () => {
    const tabs = adminTabs(true, true, { signups: 292, stillActive: 98 });
    const badge = (id: string) => tabs.find((t) => t.id === id)?.badge;
    expect(badge('analytics-users')).toEqual({ text: '292', tone: 'neutral' });
    expect(badge(ANALYTICS_TABS.abuse)).toEqual({ text: '98', tone: 'bad' });
    expect(badge('analytics-traffic')?.tone).toBe('warn');
    expect(badge('analytics-storage')?.tone).toBe('warn');
    expect(adminTabs(true, true, { stillActive: 0 }).find((t) => t.id === ANALYTICS_TABS.abuse)?.badge?.tone).toBe('neutral');
    expect(adminTabs(true, true).find((t) => t.id === ANALYTICS_TABS.abuse)?.badge).toBeUndefined();
  });

  test('an opened project lands on Projects, a tab asked for wins, an unknown one is ignored', () => {
    const tabs = adminTabs(true);
    expect(initialTab(tabs, null, 'p1')).toBe('projects');
    expect(initialTab(tabs, 'users', 'p1')).toBe('users');
    expect(initialTab(tabs, 'nope', null)).toBe('attention');
    expect(initialTab(adminTabs(false), 'users', null)).toBe('projects');
    expect(initialTab(tabs, ANALYTICS_TABS.abuse, null)).toBe('attention');
  });
});

function renderConsole(canMutate: boolean, path = '/admin', features: Record<string, boolean> = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features } } as never);
    if (url === '/admin/analytics/users') return Promise.resolve({ data: { windowDays: 30, signups: 0, suspended: 0, activeLast7Days: 0, perDay: [], funnel: [], domains: [] } } as never);
    if (url === '/admin/analytics/abuse') return Promise.resolve({ data: { windowDays: 30, perDay: [], proxyRefusals: 0, registryRefusals: 0, refusedUsers: 0, refusedStillActive: 3, suspended: 0, latest: [] } } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}><AdminConsoleV2 canMutate={canMutate} platform={<div>platform cards</div>} /></MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('AdminConsoleV2', () => {
  beforeEach(() => vi.clearAllMocks());

  test('the audit log is a tab for a platform admin and absent for an operator', async () => {
    const { unmount } = renderConsole(true, '/admin?tab=audit');
    expect(await screen.findByText('audit log list')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Audit log' })).toHaveAttribute('aria-selected', 'true');
    unmount();
    renderConsole(false, '/admin?tab=audit');
    await screen.findByText('projects list');
    expect(screen.queryByRole('tab', { name: 'Audit log' })).toBeNull();
    expect(screen.queryByText('audit log list')).toBeNull();
  });

  test('shows one list at a time and switches with a click', async () => {
    renderConsole(true);
    expect(screen.getByText('attention')).toBeInTheDocument();
    expect(screen.queryByText('projects list')).toBeNull();
    await userEvent.click(screen.getByRole('tab', { name: 'Projects' }));
    expect(screen.getByText('projects list')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Projects' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', 'admin-tab-projects');
  });

  test('arrow keys move between tabs', async () => {
    renderConsole(true);
    screen.getByRole('tab', { name: 'Needs attention' }).focus();
    await userEvent.keyboard('{ArrowRight}');
    expect(screen.getByRole('tab', { name: 'Projects' })).toHaveFocus();
    expect(screen.getByText('projects list')).toBeInTheDocument();
    await userEvent.keyboard('{ArrowLeft}{ArrowLeft}');
    expect(screen.getByRole('tab', { name: 'Platform' })).toHaveFocus();
  });

  test('an operator is never shown the people lists', () => {
    renderConsole(false, '/admin?tab=users');
    expect(screen.queryByText('users list')).toBeNull();
    expect(screen.getByText('projects list')).toBeInTheDocument();
  });

  test('with the flag on, arrow keys walk through the analytics tabs and the chips row follows them', async () => {
    renderConsole(true, '/admin?tab=audit', { adminanalytics: true });
    await screen.findByRole('tab', { name: /Sign-ups/ });
    expect(screen.queryByRole('group', { name: 'Needs attention' })).toBeNull();
    screen.getByRole('tab', { name: 'Audit log' }).focus();
    await userEvent.keyboard('{ArrowRight}');
    expect(screen.getByRole('tab', { name: /Sign-ups/ })).toHaveFocus();
    expect(screen.getByLabelText('Needs attention')).toBeInTheDocument();
  });

  test('a chip jumps to the tab that explains it', async () => {
    renderConsole(true, '/admin?tab=analytics-users', { adminanalytics: true });
    await userEvent.click(await screen.findByRole('button', { name: /3 accounts had a deploy refused/ }));
    expect(screen.getByRole('tab', { name: /Abuse/ })).toHaveAttribute('aria-selected', 'true');
  });

  test('with the flag off an analytics tab in the address is ignored and nothing is requested', async () => {
    renderConsole(true, '/admin?tab=analytics-abuse', { adminanalytics: false });
    await waitFor(() => expect(vi.mocked(api.get)).toHaveBeenCalledWith('/config'));
    expect(screen.getByText('attention')).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: /Abuse/ })).toBeNull();
    expect(vi.mocked(api.get).mock.calls.filter(([url]) => String(url).includes('analytics'))).toHaveLength(0);
  });
});
