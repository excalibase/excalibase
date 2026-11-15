import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
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

function renderShell(role = 'user') {
  useAuthStore.setState({ user: { id: 'u1', username: 'dev', email: 'dev@example.test', role }, isAuthenticated: true });
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/orgs') return Promise.resolve({ data: ORGS }) as never;
    if (url === '/provision') return Promise.resolve({ data: PROJECTS }) as never;
    return Promise.resolve({ data: { deploymentMode: 'cloud', features: { studio_v2: true } } }) as never;
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/']}>
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
    for (const name of ['Home', 'Projects', 'Usage & credits', 'Access tokens', 'Members & roles', 'Organization settings', 'Docs']) {
      expect(within(nav).getByRole('link', { name })).toBeInTheDocument();
    }
  });

  test('the admin console link is for platform admins only', async () => {
    renderShell('user');
    await screen.findByRole('navigation', { name: 'Main' });
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
    await userEvent.type(within(dialog).getByRole('combobox'), 'tokens');
    await userEvent.keyboard('{Enter}');
    expect(screen.getByTestId('where')).toHaveTextContent('/account/tokens');
    await userEvent.keyboard('{Control>}k{/Control}');
    await screen.findByRole('dialog', { name: /search/i });
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  test('the account menu signs out', async () => {
    renderShell();
    await userEvent.click(await screen.findByRole('button', { name: /account menu/i }));
    expect(screen.getByRole('menuitem', { name: /sign out/i })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: /access tokens/i })).toBeInTheDocument();
  });
});
