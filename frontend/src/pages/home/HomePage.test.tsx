import { describe, test, expect, vi, beforeEach } from 'vitest';
import { screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HomePage } from './HomePage';
import { recordVisit } from './recentlyVisited';
import { mountWith, project } from '../../test/homeFixtures';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const NOW = Date.now();
const iso = (days: number) => new Date(NOW + days * 86_400_000).toISOString();

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
});

describe('Home', () => {
  test('shows the header and every widget that has data behind it', async () => {
    mountWith({ projects: [project('p1', { projectName: 'Reading list' }), project('p2', { status: 'PAUSED' })] }, <HomePage />);
    expect(await screen.findByRole('heading', { name: 'Home' })).toBeInTheDocument();
    expect(await screen.findByText('Acme')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /new project/i })).toHaveAttribute('href', '/provision');
    for (const name of ['Recently visited', 'Health', 'Credits & usage', 'Security', 'Build something']) {
      expect(await screen.findByRole('region', { name })).toBeInTheDocument();
    }
    expect(screen.queryByRole('region', { name: 'Latest activity' })).not.toBeInTheDocument();
    const health = within(screen.getByRole('region', { name: 'Health' }));
    expect(health.getByText('Running').nextSibling).toHaveTextContent('1');
    expect(health.getByText('Paused').nextSibling).toHaveTextContent('1');
  });

  test('the attention bar shows a paused project with Resume and Delete, and hides when there is nothing', async () => {
    mountWith({ projects: [project('p2', { projectName: 'mcp-test', status: 'PAUSED', pauseReason: 'idle_7d', lastActiveAt: iso(-5) })] }, <HomePage />);
    const bar = await screen.findByRole('region', { name: 'Needs your attention' });
    expect(bar).toHaveTextContent('mcp-test');
    expect(within(bar).getByRole('button', { name: 'Resume mcp-test' })).toBeInTheDocument();
    expect(within(bar).getByRole('link', { name: 'Delete mcp-test' })).toHaveAttribute('href', '/project/p2/settings');
  });

  test('no attention bar when everything is fine', async () => {
    mountWith({ projects: [project('p1')], tokens: [{ id: 't', name: 'ok', expiresAt: iso(100), projectId: 'p1' }] }, <HomePage />);
    await screen.findByRole('region', { name: 'Health' });
    expect(screen.queryByRole('region', { name: 'Needs your attention' })).not.toBeInTheDocument();
  });

  test('tokens appear in the attention bar and the security widget', async () => {
    mountWith({
      projects: [project('p1')],
      tokens: [{ id: 't1', name: 'old-ci', expiresAt: iso(-3) }, { id: 't2', name: 'soon', expiresAt: iso(30), projectId: 'p1' }],
    }, <HomePage />);
    const bar = await screen.findByRole('region', { name: 'Needs your attention' });
    expect(bar).toHaveTextContent('old-ci');
    expect(within(bar).getByRole('link', { name: 'Revoke old-ci' })).toHaveAttribute('href', '/account/tokens');
    const security = within(screen.getByRole('region', { name: 'Security' }));
    await waitFor(() => expect(security.getByText('Tokens expiring in 60 days').nextSibling).toHaveTextContent('1'));
    expect(security.getByText('Tokens with access to every project').nextSibling).toHaveTextContent('0');
  });

  test('counts databases open to the internet, and says when some could not be checked', async () => {
    mountWith({
      projects: [project('open'), project('closed'), project('port-off')],
      allowed: {
        open: { access: 'any', sources: [], publicEnabled: true },
        closed: { access: 'listed', sources: ['1.2.3.4/32'], publicEnabled: true },
        'port-off': { access: 'any', sources: [], publicEnabled: false },
      },
    }, <HomePage />);
    const security = within(await screen.findByRole('region', { name: 'Security' }));
    await waitFor(() => expect(security.getByText('Databases open to the internet').nextSibling).toHaveTextContent('1'));
  });

  test('the security row is left out when the platform does not report allow-lists', async () => {
    mountWith({ projects: [project('p1')], features: { dballowlist: false } }, <HomePage />);
    const security = within(await screen.findByRole('region', { name: 'Security' }));
    expect(security.queryByText('Databases open to the internet')).not.toBeInTheDocument();
    expect(security.queryByText(/two-factor/i)).not.toBeInTheDocument();
  });

  test('credits shows no invented figures and links to usage', async () => {
    mountWith({ projects: [project('p1')] }, <HomePage />);
    const credits = within(await screen.findByRole('region', { name: 'Credits & usage' }));
    expect(credits.getByRole('link', { name: /usage details/i })).toHaveAttribute('href', '/usage');
    expect(credits.queryByText(/\$/)).not.toBeInTheDocument();
  });

  test('recently visited lists what this browser opened, newest first, without deleted projects', async () => {
    recordVisit('/account/tokens');
    recordVisit('/project/gone');
    recordVisit('/project/p1/database/tables');
    mountWith({ projects: [project('p1', { projectName: 'Reading list' })] }, <HomePage />);
    const recent = within(await screen.findByRole('region', { name: 'Recently visited' }));
    await waitFor(() => expect(recent.getAllByRole('listitem')).toHaveLength(2));
    const items = recent.getAllByRole('listitem');
    expect(items[0]).toHaveTextContent('Reading list › Tables');
    expect(items[1]).toHaveTextContent('Access tokens');
    expect(recent.queryByText(/gone/)).not.toBeInTheDocument();
  });

  test('build links open the create flows, only for what is switched on', async () => {
    mountWith({ projects: [project('p1')], features: { mcp: false } }, <HomePage />);
    const build = within(await screen.findByRole('region', { name: 'Build something' }));
    expect(build.getByRole('link', { name: /Create a Postgres database/ })).toHaveAttribute('href', '/provision');
    await waitFor(() => expect(build.getByRole('link', { name: /container image/ })).toHaveAttribute('href', '/project/p1/containers/new'));
    expect(build.getByRole('link', { name: /static site/ })).toHaveAttribute('href', '/project/p1/sites/new');
    expect(build.queryByRole('link', { name: /AI coding tool/ })).not.toBeInTheDocument();
  });

  test('widgets can be hidden and the choice is remembered', async () => {
    mountWith({ projects: [project('p1')] }, <HomePage />);
    await screen.findByRole('region', { name: 'Security' });
    await userEvent.click(screen.getByRole('button', { name: /customize widgets/i }));
    await userEvent.click(screen.getByRole('checkbox', { name: 'Security' }));
    expect(screen.queryByRole('region', { name: 'Security' })).not.toBeInTheDocument();
    expect(JSON.parse(localStorage.getItem('studio.home.hiddenWidgets') ?? '[]')).toEqual(['security']);
  });

  test('a failed project load says so instead of showing zeros', async () => {
    mountWith({ projects: [], failProjects: true }, <HomePage />);
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not load your projects/i);
    expect(screen.queryByRole('region', { name: 'Health' })).not.toBeInTheDocument();
  });
});
