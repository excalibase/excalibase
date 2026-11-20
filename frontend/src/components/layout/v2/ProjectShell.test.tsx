import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
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

  test('the tab bar offers Services, Usage, Activity and Settings, each a real page', async () => {
    renderShell('/project/p1');
    const tabs = await screen.findByRole('navigation', { name: 'Project' });
    expect(within(tabs).getAllByRole('link').map((a) => a.textContent)).toEqual(['Services', 'Usage', 'Activity', 'Settings']);
    expect(within(tabs).getByRole('link', { name: 'Usage' })).toHaveAttribute('href', '/project/p1/monitoring/metrics');
    expect(within(tabs).getByRole('link', { name: 'Activity' })).toHaveAttribute('href', '/project/p1/monitoring/logs');
  });

  test('activeTab matches sub-paths and leaves unlisted pages unmarked', () => {
    expect(activeTab('/project/p1', 'p1')).toBe('');
    expect(activeTab('/project/p1/monitoring/metrics', 'p1')).toBe('monitoring/metrics');
    expect(activeTab('/project/p1/sql', 'p1')).toBeUndefined();
  });
});
