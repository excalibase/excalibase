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
    expect(adminTabs(true).map((t) => t.id)).toEqual(['attention', 'projects', 'users', 'orgs', 'platform']);
  });

  test('the audit log tab is for a platform admin on studio v2 only', () => {
    expect(adminTabs(true, false, {}, true).map((t) => t.id)).toEqual(['attention', 'projects', 'users', 'orgs', 'audit', 'platform']);
    expect(adminTabs(true).map((t) => t.id)).not.toContain('audit');
    expect(adminTabs(false, false, {}, true).map((t) => t.id)).toEqual(['projects', 'platform']);
  });

  test('an operator sees projects and the platform cards, never the people lists', () => {
    expect(adminTabs(false).map((t) => t.id)).toEqual(['projects', 'platform']);
  });

  test('the first analytics tab starts a new group', () => {
    expect(adminTabs(true, true).filter((t) => t.startsGroup).map((t) => t.id)).toEqual(['analytics-users']);
  });

  test('the analytics tabs sit before the platform cards and only for a platform admin', () => {
    expect(adminTabs(true, true).map((t) => t.id)).toEqual([
      'attention', 'projects', 'users', 'orgs', 'analytics-users', ANALYTICS_TABS.abuse, 'analytics-traffic', 'analytics-storage', 'platform',
    ]);
    expect(adminTabs(false, true).map((t) => t.id)).toEqual(['projects', 'platform']);
  });

  test('the credit tab follows the storage tab, for a platform admin with credits on', () => {
    const ids = (credits: boolean, canMutate = true) => adminTabs(canMutate, true, {}, false, credits).map((t) => t.id);
    expect(ids(true)).toEqual([
      'attention', 'projects', 'users', 'orgs', 'analytics-users', ANALYTICS_TABS.abuse, 'analytics-traffic', 'analytics-storage', 'analytics-credits', 'platform',
    ]);
    expect(ids(false)).not.toContain('analytics-credits');
    expect(ids(true, false)).toEqual(['projects', 'platform']);
    expect(adminTabs(true, false, {}, false, true).map((t) => t.id)).not.toContain('analytics-credits');
  });

  test('the credit tab shows how many orgs are at $0 beside its name', () => {
    const tab = adminTabs(true, true, { orgsAtZero: 17 }, false, true).find((t) => t.id === ANALYTICS_TABS.credits);
    expect(tab?.badge).toEqual({ text: '17', tone: 'neutral' });
    expect(adminTabs(true, true, {}, false, true).find((t) => t.id === ANALYTICS_TABS.credits)?.badge).toBeUndefined();
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
    if (url === '/admin/analytics/credits') return Promise.resolve({ data: { orgsWithCredit: 120, orgsAtZero: 17, orgsWithPausedServices: 5, servicesPaused: 9 } } as never);
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

  test('with studio v2 off the audit log tab is not offered and its address is ignored', async () => {
    renderConsole(true, '/admin?tab=audit', { studio_v2: false });
    await waitFor(() => expect(vi.mocked(api.get)).toHaveBeenCalledWith('/config'));
    expect(screen.getByText('attention')).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: 'Audit log' })).toBeNull();
    expect(screen.queryByText('audit log list')).toBeNull();
  });

  test('the audit log is a tab for a platform admin and absent for an operator', async () => {
    const { unmount } = renderConsole(true, '/admin?tab=audit', { studio_v2: true });
    expect(await screen.findByText('audit log list')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Audit log' })).toHaveAttribute('aria-selected', 'true');
    unmount();
    renderConsole(false, '/admin?tab=audit', { studio_v2: true });
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
    renderConsole(true, '/admin?tab=audit', { adminanalytics: true, studio_v2: true });
    await screen.findByRole('tab', { name: /Sign-ups/ });
    expect(screen.queryByRole('group', { name: 'Needs attention' })).toBeNull();
    screen.getByRole('tab', { name: 'Audit log' }).focus();
    await userEvent.keyboard('{ArrowRight}');
    expect(screen.getByRole('tab', { name: /Sign-ups/ })).toHaveFocus();
    expect(screen.getByLabelText('Needs attention')).toBeInTheDocument();
  });

  test('with credits on the Credit tab opens, shows its numbers and wears the count of orgs at $0', async () => {
    renderConsole(true, '/admin?tab=analytics-credits', { adminanalytics: true, credits: true });
    expect(await screen.findByText('Orgs at $0')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /Credit/ })).toHaveTextContent('17');
    expect(screen.getByText('Orgs at $0').nextElementSibling).toHaveTextContent('17');
  });

  test('with credits off there is no Credit tab and the credit numbers are never requested', async () => {
    renderConsole(true, '/admin?tab=analytics-credits', { adminanalytics: true, credits: false });
    await screen.findByRole('tab', { name: /Sign-ups/ });
    await waitFor(() => expect(vi.mocked(api.get)).toHaveBeenCalledWith('/admin/analytics/users'));
    expect(screen.queryByRole('tab', { name: /Credit/ })).toBeNull();
    expect(vi.mocked(api.get).mock.calls.some(([url]) => String(url).includes('analytics/credits'))).toBe(false);
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
