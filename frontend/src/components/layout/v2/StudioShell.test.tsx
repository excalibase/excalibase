import { describe, test, expect, vi, beforeEach } from 'vitest';
import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Link, MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { OrgsPage } from '../../../pages/OrgsPage';
import { StudioShell } from './StudioShell';
import { api } from '../../../api/client';
import { useAuthStore } from '../../../stores/auth-store';
import { useOrgSelection } from '../../../stores/org-selection';

// jsdom has neither, and the palette's list reads both.
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };
Element.prototype.scrollIntoView ??= () => {};

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const ORGS = [
  { id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: 'u1', role: 'owner' },
  { id: 'org-2', name: 'Globex', slug: 'globex', tier: 'STANDARD', ownerId: 'u9', role: 'developer' },
  { id: 'org-other', name: 'Not Mine', slug: 'nm', tier: 'FREE', ownerId: 'u9' },
];
const PROJECTS = [
  { projectId: 'reading-list', projectName: 'Reading list', orgId: 'org-1', tier: 'FREE', status: 'ACTIVE', databaseType: 'POSTGRESQL' },
  { projectId: 'foreign-app', orgId: 'org-other', tier: 'FREE', status: 'ACTIVE', databaseType: 'POSTGRESQL' },
];

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>;
}

// Every answer the mocked API has given; `settle` waits for them and for the screen to take them in,
// so an assertion that something is absent cannot pass just because nothing has loaded yet.
const answers: Promise<unknown>[] = [];
async function settle() {
  await act(async () => {
    await Promise.allSettled(answers);
    await new Promise((resolve) => setTimeout(resolve, 20));
  });
}

interface Setup { readonly orgs?: unknown[]; readonly credits?: object; readonly features?: Record<string, boolean>; readonly path?: string; readonly adminHost?: string; readonly orgsFail?: boolean; readonly orgsHang?: boolean }

function renderShell(role = 'user', { orgs = ORGS, credits, features = {}, path = '/', adminHost, orgsFail, orgsHang }: Setup = {}) {
  answers.length = 0;
  useAuthStore.setState({ user: { id: 'u1', username: 'dev', email: 'dev@example.test', role }, isAuthenticated: true });
  vi.mocked(api.get).mockImplementation((url: string) => {
    let answer: Promise<unknown>;
    if (url === '/orgs') {
      if (orgsHang) return new Promise(() => {}) as never;
      answer = orgsFail ? Promise.reject(new Error('orgs down')) : Promise.resolve({ data: orgs });
    } else if (url === '/provision') answer = Promise.resolve({ data: PROJECTS });
    else if (url === '/orgs/org-1/credits/') answer = credits ? Promise.resolve({ data: credits }) : Promise.reject(new Error('credits down'));
    else answer = Promise.resolve({ data: { deploymentMode: 'cloud', adminHost, features: { studio_v2: true, ...features } } });
    answers.push(answer);
    return answer as never;
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route element={<StudioShell />}>
            <Route path="*" element={<Where />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('StudioShell', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useOrgSelection.setState({ orgId: null });
  });

  test('left nav lists the console destinations', async () => {
    renderShell();
    const nav = await screen.findByRole('navigation', { name: 'Main' });
    for (const name of ['Home', 'Projects', 'Usage & credits', 'Access keys', 'Access (IAM)', 'Organization settings', 'Docs']) {
      expect(within(nav).getByRole('link', { name })).toBeInTheDocument();
    }
  });

  test('the admin console link is for platform admins only', async () => {
    renderShell('user');
    await screen.findByRole('navigation', { name: 'Main' });
    await settle();
    expect(screen.queryByRole('link', { name: /admin console/i })).not.toBeInTheDocument();
  });

  test('a platform admin sees the admin console link', async () => {
    renderShell('platform_admin');
    expect(await screen.findByRole('link', { name: /admin console/i })).toHaveAttribute('href', '/admin');
  });

  test('the org switcher shows name and plan and lists only my orgs', async () => {
    renderShell();
    const button = await screen.findByRole('button', { name: /organization: acme/i });
    expect(button).toHaveTextContent('Free');
    await userEvent.click(button);
    const options = within(screen.getByRole('listbox', { name: /organizations/i })).getAllByRole('option');
    expect(options.map((o) => o.textContent)).toEqual([expect.stringContaining('Acme'), expect.stringContaining('Globex')]);
    expect(screen.queryByText('Not Mine')).not.toBeInTheDocument();
  });

  test('choosing another org switches the shown org', async () => {
    renderShell();
    await userEvent.click(await screen.findByRole('button', { name: /organization: acme/i }));
    await userEvent.click(screen.getByRole('option', { name: /globex/i }));
    expect(await screen.findByRole('button', { name: /organization: globex/i })).toHaveTextContent('Standard');
  });

  test('Ctrl+K opens the palette, which finds my projects but not other orgs projects', async () => {
    renderShell();
    await screen.findByRole('navigation', { name: 'Main' });
    await userEvent.keyboard('{Control>}k{/Control}');
    const dialog = await screen.findByRole('dialog', { name: /search/i });
    await within(dialog).findByText('Reading list');
    expect(within(dialog).queryByText('foreign-app')).not.toBeInTheDocument();
    await userEvent.type(within(dialog).getByRole('combobox'), 'reading');
    await userEvent.keyboard('{Enter}');
    expect(screen.getByTestId('where')).toHaveTextContent('/project/reading-list');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  test('the palette finds nav destinations and Escape closes it', async () => {
    renderShell();
    await userEvent.click(await screen.findByRole('button', { name: /search/i }));
    const dialog = await screen.findByRole('dialog', { name: /search/i });
    await userEvent.type(within(dialog).getByRole('combobox'), 'access keys');
    await userEvent.keyboard('{Enter}');
    expect(screen.getByTestId('where')).toHaveTextContent('/account/tokens');
    await userEvent.keyboard('{Control>}k{/Control}');
    await screen.findByRole('dialog', { name: /search/i });
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  // No notifications API exists in the platform (server-go has no such route), so the bell stays hidden.
  test('the top bar has a help link and no notification bell', async () => {
    renderShell();
    expect(await screen.findByRole('link', { name: /help and docs/i })).toHaveAttribute('href', expect.stringContaining('/docs'));
    await settle();
    expect(screen.queryByRole('button', { name: /notifications/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/no notifications/i)).not.toBeInTheDocument();
  });

  test('the left nav links to the Organizations list', async () => {
    renderShell();
    const nav = await screen.findByRole('navigation', { name: 'Main' });
    expect(within(nav).getByRole('link', { name: 'Organizations' })).toHaveAttribute('href', '/orgs');
  });

  test('the org switcher offers the list and a create-organization entry', async () => {
    renderShell();
    await userEvent.click(await screen.findByRole('button', { name: /organization: acme/i }));
    expect(screen.getByRole('link', { name: /all organizations/i })).toHaveAttribute('href', '/orgs');
    expect(screen.getByRole('link', { name: /create organization/i })).toHaveAttribute('href', '/orgs?new=1');
  });

  test('a platform admin on a host-monitoring install gets the Host link', async () => {
    renderShell('platform_admin', { features: { hostmetrics: true } });
    expect(await screen.findByRole('link', { name: 'Host' })).toHaveAttribute('href', '/admin/host');
  });

  test('Host is hidden from non-admins, and where host monitoring is off', async () => {
    renderShell('user', { features: { hostmetrics: true } });
    await screen.findByRole('navigation', { name: 'Main' });
    await settle();
    expect(screen.queryByRole('link', { name: 'Host' })).not.toBeInTheDocument();
  });

  test('Host is hidden when host monitoring is off', async () => {
    renderShell('platform_admin');
    await screen.findByRole('link', { name: /admin console/i });
    await settle();
    expect(screen.queryByRole('link', { name: 'Host' })).not.toBeInTheDocument();
  });

  test('the admin links are hidden on a host that is not the admin host', async () => {
    renderShell('platform_admin', { features: { hostmetrics: true }, adminHost: 'admin.example.test' });
    await screen.findByRole('navigation', { name: 'Main' });
    expect(await screen.findByRole('link', { name: 'Organizations' })).toBeInTheDocument();
    await settle();
    expect(screen.queryByRole('link', { name: 'Host' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /admin console/i })).not.toBeInTheDocument();
  });

  test('a non-admin on the admin host gets no Admin console or Host link', async () => {
    renderShell('user', { features: { hostmetrics: true }, adminHost: 'localhost' });
    await screen.findByRole('navigation', { name: 'Main' });
    await settle();
    expect(screen.queryByRole('link', { name: 'Host' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /admin console/i })).not.toBeInTheDocument();
  });

  test('an admin on the admin host gets both', async () => {
    renderShell('platform_admin', { features: { hostmetrics: true }, adminHost: 'localhost' });
    expect(await screen.findByRole('link', { name: 'Host' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /admin console/i })).toBeInTheDocument();
  });

  test('a person with no organization sees a page to create one or accept an invite', async () => {
    renderShell('user', { orgs: [] });
    expect(await screen.findByRole('heading', { name: /join or create an organization/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /create an organization/i })).toHaveAttribute('href', '/orgs?new=1');
    expect(screen.getByLabelText(/invite link or code/i)).toBeInTheDocument();
    expect(screen.queryByTestId('where')).not.toBeInTheDocument();
  });

  test('a person with organizations sees the normal page once they have loaded', async () => {
    renderShell('user');
    expect(await screen.findByTestId('where')).toHaveTextContent('/');
    await settle();
    expect(screen.queryByRole('heading', { name: /join or create an organization/i })).not.toBeInTheDocument();
  });

  test('while the organizations load, the no-organization page is not shown', async () => {
    renderShell('user', { orgsHang: true });
    expect(await screen.findByText(/loading organization/i)).toBeInTheDocument();
    await settle();
    expect(screen.queryByRole('heading', { name: /join or create an organization/i })).not.toBeInTheDocument();
  });

  test('when the organizations fail to load, the no-organization page is not shown', async () => {
    renderShell('user', { orgsFail: true });
    expect(await screen.findByRole('alert')).toHaveTextContent(/organization unavailable/i);
    await settle();
    expect(screen.queryByRole('heading', { name: /join or create an organization/i })).not.toBeInTheDocument();
  });

  test('a person who creates their first organization then sees the normal Home, not the no-organization page', async () => {
    let created = false;
    const org = { id: 'org-new', name: 'Fresh', slug: 'fresh', tier: 'FREE', ownerId: 'u1', role: 'owner' };
    useAuthStore.setState({ user: { id: 'u1', username: 'dev', email: 'd@e.test', role: 'user' }, isAuthenticated: true });
    vi.mocked(api.get).mockImplementation((url: string) => {
      if (url === '/orgs') return Promise.resolve({ data: created ? [org] : [] }) as never;
      if (url === '/provision') return Promise.resolve({ data: [] }) as never;
      return Promise.resolve({ data: { deploymentMode: 'cloud', features: { studio_v2: true } } }) as never;
    });
    vi.mocked(api.post).mockImplementation(() => { created = true; return Promise.resolve({ data: org }) as never; });
    function Detail() { return <Link to="/">home</Link>; }
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter initialEntries={['/']}>
          <Routes>
            <Route element={<StudioShell />}>
              <Route path="/" element={<Where />} />
              <Route path="/orgs" element={<OrgsPage />} />
              <Route path="/orgs/:orgId" element={<Detail />} />
            </Route>
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await userEvent.click(await screen.findByRole('link', { name: /create an organization/i }));
    await userEvent.type(await screen.findByPlaceholderText('My Company'), 'Fresh');
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    await userEvent.click(await screen.findByRole('link', { name: 'home' }));
    expect(await screen.findByTestId('where')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: /join or create an organization/i })).not.toBeInTheDocument();
  });

  test('the no-organization page does not cover the pages that need no org', async () => {
    renderShell('user', { orgs: [], path: '/account/tokens' });
    expect(await screen.findByTestId('where')).toHaveTextContent('/account/tokens');
    await settle();
    expect(screen.queryByRole('heading', { name: /join or create an organization/i })).not.toBeInTheDocument();
  });

  test('the org switcher of a person with no organization links to create one', async () => {
    renderShell('user', { orgs: [] });
    expect(await screen.findByRole('link', { name: /no organization/i })).toHaveAttribute('href', '/orgs?new=1');
  });

  test('the palette finds docs links', async () => {
    renderShell();
    await userEvent.click(await screen.findByRole('button', { name: /search/i }));
    const dialog = await screen.findByRole('dialog', { name: /search/i });
    await userEvent.type(within(dialog).getByRole('combobox'), 'documentation');
    expect(within(dialog).getByText('Documentation')).toBeInTheDocument();
  });

  test('the account menu opens the Account page', async () => {
    renderShell();
    await userEvent.click(await screen.findByRole('button', { name: /account menu/i }));
    expect(screen.getByRole('menuitem', { name: 'Account' })).toHaveAttribute('href', '/account');
  });

  test('the account menu signs out', async () => {
    renderShell();
    await userEvent.click(await screen.findByRole('button', { name: /account menu/i }));
    expect(screen.getByRole('menuitem', { name: /sign out/i })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: /access keys/i })).toBeInTheDocument();
  });

  describe('credit banner', () => {
    const zero = (extra: object) => ({ atZero: false, lowCredit: false, graceEndsAt: null, graceDays: 7, paused: [], ...extra });
    const credit = (balanceMicros: number, extra: object = {}) => ({ balanceMicros, metering: true, nextExpiry: '2027-03-01T00:00:00Z', ...extra });
    const nav = () => screen.findByRole('navigation', { name: 'Main' });

    test('used up, in the grace period, it names the pause date and links to Usage & credits', async () => {
      renderShell('user', { features: { credits: true }, credits: credit(0, { zero: zero({ atZero: true, graceEndsAt: '2099-01-10T00:00:00Z' }) }) });
      const banner = await screen.findByTestId('credit-banner');
      expect(banner).toHaveTextContent(/credit is used up/i);
      expect(banner).toHaveTextContent(new Date('2099-01-10T00:00:00Z').toLocaleDateString());
      expect(within(banner).getByRole('link', { name: 'Usage & credits' })).toHaveAttribute('href', '/usage');
    });

    test('past the grace period with nothing paused it does not claim anything is paused', async () => {
      renderShell('user', { features: { credits: true }, credits: credit(0, { zero: zero({ atZero: true, graceEndsAt: '2020-01-10T00:00:00Z' }) }) });
      expect(await screen.findByTestId('credit-banner')).toHaveTextContent(/nothing is paused/i);
    });

    test('past the grace period with something paused it says compute is paused and who to write to', async () => {
      const paused = [{ kind: 'app' as const, projectId: 'p1', serviceId: 'a1', name: 'web', pausedAt: '2020-01-10T00:00:00Z' }];
      renderShell('user', { features: { credits: true }, credits: credit(0, { zero: zero({ atZero: true, graceEndsAt: '2020-01-10T00:00:00Z', paused }) }) });
      const banner = await screen.findByTestId('credit-banner');
      expect(banner).toHaveTextContent(/is paused and your data is kept/i);
      expect(banner).toHaveTextContent(/contact support to add credit: hello@excalibase\.io/i);
    });

    test('low credit shows the amount left', async () => {
      renderShell('user', { features: { credits: true }, credits: credit(40_000_000, { zero: zero({ lowCredit: true }) }) });
      expect(await screen.findByTestId('credit-banner')).toHaveTextContent('$40.00 left');
    });

    test('healthy credit shows no banner once everything has loaded', async () => {
      renderShell('user', { features: { credits: true }, credits: credit(300_000_000, { zero: zero({}) }) });
      await nav();
      await settle();
      expect(vi.mocked(api.get)).toHaveBeenCalledWith('/orgs/org-1/credits/');
      expect(screen.queryByTestId('credit-banner')).toBeNull();
    });

    test('with credits off it asks for nothing and shows nothing', async () => {
      renderShell('user', { features: {}, credits: credit(0, { zero: zero({ atZero: true, graceEndsAt: '2099-01-10T00:00:00Z' }) }) });
      await nav();
      await settle();
      expect(vi.mocked(api.get).mock.calls.some(([url]) => String(url).includes('/credits'))).toBe(false);
      expect(screen.queryByTestId('credit-banner')).toBeNull();
    });

    test('a failed credit read leaves the console alone', async () => {
      renderShell('user', { features: { credits: true } });
      await nav();
      await settle();
      expect(screen.queryByTestId('credit-banner')).toBeNull();
    });
  });
});
