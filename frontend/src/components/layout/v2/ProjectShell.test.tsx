import { describe, test, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, within, act, cleanup } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ProjectShell } from './ProjectShell';
import { activeTab } from './ProjectBar';
import { api } from '../../../api/client';
import { useAuthStore } from '../../../stores/auth-store';

globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };
Element.prototype.scrollIntoView ??= () => {};

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const PROJECTS = [
  { projectId: 'p1', projectName: 'Reading list', orgId: 'org-1', tier: 'FREE', status: 'ACTIVE', databaseType: 'POSTGRESQL' },
  { projectId: 'p2', orgId: 'org-1', tier: 'FREE', status: 'ACTIVE', databaseType: 'POSTGRESQL' },
];

function renderShell(path: string) {
  useAuthStore.setState({ user: { id: 'u1', username: 'dev', email: 'd@e.test', role: 'user' }, isAuthenticated: true });
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/orgs') return Promise.resolve({ data: [{ id: 'org-1', name: 'Acme', slug: 'a', tier: 'FREE', ownerId: 'u1', role: 'owner' }] }) as never;
    if (url === '/provision') return Promise.resolve({ data: PROJECTS }) as never;
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features: { studio_v2: true } } }) as never;
    return Promise.resolve({ data: PROJECTS[0] }) as never;
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/project/:projectId" element={<ProjectShell />}>
            <Route index element={<p>services page</p>} />
            <Route path="settings" element={<p>settings page</p>} />
            <Route path="sql" element={<p>sql page</p>} />
            <Route path="database/overview" element={<p>database page</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('ProjectShell', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows Org / Project and the tab bar with the current tab marked', async () => {
    renderShell('/project/p1');
    const crumb = await screen.findByRole('navigation', { name: 'Breadcrumb' });
    expect(await within(crumb).findByRole('link', { name: 'Acme' })).toHaveAttribute('href', '/instances');
    expect(await within(crumb).findByRole('button', { name: 'Project: Reading list' })).toBeInTheDocument();
    const tabs = screen.getByRole('navigation', { name: 'Project' });
    expect(within(tabs).getByRole('link', { name: 'Services' })).toHaveAttribute('aria-current', 'page');
    expect(within(tabs).getByRole('link', { name: 'Settings' })).not.toHaveAttribute('aria-current');
  });

  test('the project switcher lists my projects and keeps the page', async () => {
    renderShell('/project/p1/settings');
    await userEvent.click(await screen.findByRole('button', { name: /^Project:/ }));
    const options = within(await screen.findByRole('listbox', { name: 'Projects' })).getAllByRole('option');
    expect(options.map((o) => o.textContent)).toEqual(['Reading list', 'p2']);
    await userEvent.click(options[1]);
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  });

  test('the tab bar offers Services, Variables, Metrics, Logs and Settings, each named for the page it opens', async () => {
    renderShell('/project/p1');
    const tabs = await screen.findByRole('navigation', { name: 'Project' });
    expect(within(tabs).getAllByRole('link').map((a) => a.textContent)).toEqual(['Services', 'Variables', 'Metrics', 'Logs', 'Settings']);
    expect(within(tabs).getByRole('link', { name: 'Variables' })).toHaveAttribute('href', '/project/p1/variables');
    expect(within(tabs).getByRole('link', { name: 'Metrics' })).toHaveAttribute('href', '/project/p1/monitoring/metrics');
    expect(within(tabs).getByRole('link', { name: 'Logs' })).toHaveAttribute('href', '/project/p1/monitoring/logs');
  });

  test('activeTab matches sub-paths and leaves unlisted pages unmarked', () => {
    expect(activeTab('/project/p1', 'p1')).toBe('');
    expect(activeTab('/project/p1/monitoring/metrics', 'p1')).toBe('monitoring/metrics');
    expect(activeTab('/project/p1/variables', 'p1')).toBe('variables');
    expect(activeTab('/project/p1/sql', 'p1')).toBeUndefined();
  });
});

const PHONE = '(max-width: 767.98px)';
let phoneNow = false;
const listeners = new Set<() => void>();

function setPhone(matches: boolean) {
  phoneNow = matches;
  listeners.clear();
  Object.defineProperty(globalThis.window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => ({
      get matches() { return phoneNow && query === PHONE; },
      media: query,
      addEventListener: (_: string, listener: () => void) => listeners.add(listener),
      removeEventListener: (_: string, listener: () => void) => listeners.delete(listener),
    }),
  });
}

// The window crosses the breakpoint: the query changes and tells its listeners.
function resizeTo(phone: boolean) {
  act(() => { phoneNow = phone; listeners.forEach((listener) => listener()); });
}

const openDrawer = async () => {
  const menu = await screen.findByRole('button', { name: 'Menu' });
  await userEvent.click(menu);
  return { menu, drawer: screen.getByRole('dialog', { name: 'Project navigation' }) };
};

describe('ProjectShell on a phone', () => {
  beforeEach(() => { vi.clearAllMocks(); setPhone(true); });
  afterEach(() => setPhone(false));

  test('swaps the rail and sub-navigation for a menu button that starts closed', async () => {
    renderShell('/project/p1');
    const menu = await screen.findByRole('button', { name: 'Menu' });
    expect(menu).toHaveAttribute('aria-expanded', 'false');
    expect(menu).not.toHaveAttribute('aria-controls');
    expect(screen.queryByTestId('icon-rail')).not.toBeInTheDocument();
    expect(screen.queryByTestId('sub-nav')).not.toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.getByText('services page')).toBeInTheDocument();
  });

  test('content gets phone padding, not the desktop one', async () => {
    renderShell('/project/p1');
    await screen.findByRole('button', { name: 'Menu' });
    expect(screen.getByRole('main')).toHaveClass('p-4');
    expect(screen.getByRole('main')).not.toHaveClass('p-6');
  });

  test('the button opens a modal drawer with the rail and moves focus into it', async () => {
    renderShell('/project/p1');
    const { menu, drawer } = await openDrawer();
    expect(menu).toHaveAttribute('aria-expanded', 'true');
    expect(menu).toHaveAttribute('aria-controls', drawer.id);
    expect(drawer).toHaveAttribute('aria-modal', 'true');
    expect(within(drawer).getByTestId('icon-rail')).toBeInTheDocument();
    expect(within(drawer).queryByTestId('sidebar-settings')).not.toBeInTheDocument();
    expect(drawer).toContainElement(document.activeElement as HTMLElement);
  });

  test('a page with a sub-navigation shows it inside the drawer only', async () => {
    renderShell('/project/p1/database/overview');
    const { drawer } = await openDrawer();
    expect(within(drawer).getByTestId('sub-nav')).toBeInTheDocument();
    expect(screen.getAllByTestId('sub-nav')).toHaveLength(1);
  });

  test('everything behind the drawer is inert while it is open', async () => {
    renderShell('/project/p1');
    const { drawer } = await openDrawer();
    const shell = screen.getByTestId('studio-shell');
    const behind = [...shell.children].filter((child) => !child.contains(drawer));
    expect(behind.length).toBeGreaterThan(2);
    for (const child of behind) expect(child).toHaveAttribute('inert');
    await userEvent.keyboard('{Escape}');
    for (const child of behind) expect(child).not.toHaveAttribute('inert');
  });

  test('Escape closes the drawer and returns focus to the button', async () => {
    renderShell('/project/p1');
    const { menu } = await openDrawer();
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(menu).toHaveAttribute('aria-expanded', 'false');
    expect(menu).toHaveFocus();
  });

  test('Escape works even when focus has left the drawer', async () => {
    renderShell('/project/p1');
    await openDrawer();
    (document.activeElement as HTMLElement).blur();
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  test('Tab with focus outside the drawer brings it back inside', async () => {
    renderShell('/project/p1');
    const { drawer } = await openDrawer();
    (document.activeElement as HTMLElement).blur();
    await userEvent.tab();
    expect(drawer).toContainElement(document.activeElement as HTMLElement);
  });

  test('the drawer keeps the page behind it from scrolling and drops its key listener on close', async () => {
    const remove = vi.spyOn(document, 'removeEventListener');
    renderShell('/project/p1');
    const { drawer } = await openDrawer();
    expect(drawer).toHaveClass('overscroll-contain');
    await userEvent.keyboard('{Escape}');
    expect(remove).toHaveBeenCalledWith('keydown', expect.any(Function));
    remove.mockRestore();
  });

  test('opening the search with Ctrl+K closes the drawer first and focuses the search box', async () => {
    renderShell('/project/p1');
    const { menu } = await openDrawer();
    await userEvent.keyboard('{Control>}k{/Control}');
    const search = await screen.findByRole('dialog', { name: 'Search' });
    expect(screen.queryByRole('dialog', { name: 'Project navigation' })).not.toBeInTheDocument();
    expect(menu).toHaveAttribute('aria-expanded', 'false');
    expect(within(search).getByRole('combobox')).toHaveFocus();
    for (const child of screen.getByTestId('studio-shell').children) expect(child).not.toHaveAttribute('inert');
  });

  test('drawer, Ctrl+K, Escape returns focus to the menu button', async () => {
    renderShell('/project/p1');
    const { menu } = await openDrawer();
    await userEvent.keyboard('{Control>}k{/Control}');
    await screen.findByRole('dialog', { name: 'Search' });
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(menu).toHaveFocus();
  });

  test('Ctrl+K and Escape without the drawer returns focus to what had it', async () => {
    renderShell('/project/p1');
    await screen.findByRole('button', { name: 'Menu' });
    const opener = screen.getByRole('button', { name: 'Search' });
    opener.focus();
    await userEvent.keyboard('{Control>}k{/Control}');
    await screen.findByRole('dialog', { name: 'Search' });
    await userEvent.keyboard('{Escape}');
    expect(opener).toHaveFocus();
  });

  test('the backdrop closes the drawer', async () => {
    renderShell('/project/p1');
    await openDrawer();
    await userEvent.click(screen.getByRole('button', { name: 'Close menu' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  test('navigating from the drawer closes it and puts focus on the menu button', async () => {
    renderShell('/project/p1');
    const { menu, drawer } = await openDrawer();
    await userEvent.click(within(drawer).getByTestId('nav-sql'));
    expect(await screen.findByText('sql page')).toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(menu).toHaveFocus();
  });

  test('Tab wraps inside the drawer in both directions', async () => {
    renderShell('/project/p1');
    const { drawer } = await openDrawer();
    const focusable = [...drawer.querySelectorAll<HTMLElement>('a[href], button:not([disabled])')];
    const last = focusable.at(-1) as HTMLElement;
    last.focus();
    await userEvent.tab();
    expect(focusable[0]).toHaveFocus();
    await userEvent.tab({ shift: true });
    expect(last).toHaveFocus();
  });

  test('widening the window past the breakpoint closes the drawer and brings the rail back', async () => {
    renderShell('/project/p1');
    await openDrawer();
    resizeTo(false);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Menu' })).not.toBeInTheDocument();
    expect(screen.getByTestId('icon-rail')).toBeInTheDocument();
    expect(screen.getByRole('main')).toHaveClass('p-6');
  });

  test('stops listening for window changes when the shell goes away', async () => {
    renderShell('/project/p1');
    await screen.findByRole('button', { name: 'Menu' });
    expect(listeners.size).toBeGreaterThan(0);
    cleanup();
    expect(listeners.size).toBe(0);
  });
});

describe('ProjectShell on a desktop', () => {
  beforeEach(() => { vi.clearAllMocks(); setPhone(false); });

  test('keeps the rail, the mode control and the desktop padding, and has no menu button', async () => {
    renderShell('/project/p1');
    expect(await screen.findByTestId('icon-rail')).toBeInTheDocument();
    expect(screen.getByTestId('sidebar-settings')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Menu' })).not.toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.getByRole('main')).toHaveClass('p-6');
  });

  test('shows the sub-navigation beside the content', async () => {
    renderShell('/project/p1/database/overview');
    expect(await screen.findByTestId('sub-nav')).toBeInTheDocument();
  });
});
