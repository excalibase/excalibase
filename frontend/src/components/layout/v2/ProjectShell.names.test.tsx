import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ProjectShell } from './ProjectShell';
import { api } from '../../../api/client';
import { useAuthStore } from '../../../stores/auth-store';

globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };
Element.prototype.scrollIntoView ??= () => {};

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

// EXC-870: the phone drawer and the rail name services as the v2 console does.
const PROJECT = { projectId: 'p1', projectName: 'Reading list', orgId: 'org-1', tier: 'FREE', status: 'ACTIVE', databaseType: 'POSTGRESQL' };

function renderShell() {
  useAuthStore.setState({ user: { id: 'u1', username: 'ops', email: 'o@e.test', role: 'platform_admin' }, isAuthenticated: true });
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/orgs') return Promise.resolve({ data: [{ id: 'org-1', name: 'Acme', slug: 'a', tier: 'FREE', ownerId: 'u1', role: 'owner' }] }) as never;
    if (url === '/provision') return Promise.resolve({ data: [PROJECT] }) as never;
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', appHosting: true, features: { studio_v2: true } } }) as never;
    return Promise.resolve({ data: PROJECT }) as never;
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={['/project/p1/database/overview']}>
        <Routes>
          <Route path="/project/:projectId" element={<ProjectShell />}>
            <Route path="database/overview" element={<p>database page</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const PHONE = '(max-width: 767.98px)';

function setPhone(phone: boolean) {
  Object.defineProperty(globalThis.window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => ({ matches: phone && query === PHONE, media: query, addEventListener: () => {}, removeEventListener: () => {} }),
  });
}

describe('project navigation names (studio_v2)', () => {
  beforeEach(() => vi.clearAllMocks());

  test('the phone drawer says Database, Apps and Secrets, not Databases, Containers and Vault', async () => {
    setPhone(true);
    renderShell();
    await userEvent.click(await screen.findByRole('button', { name: 'Menu' }));
    const drawer = screen.getByRole('dialog', { name: 'Project navigation' });
    expect(await within(drawer).findByTestId('nav-containers')).toHaveTextContent('Apps');
    expect(within(drawer).getByTestId('nav-database')).toHaveTextContent('Database');
    expect(within(drawer).getByTestId('nav-vault')).toHaveTextContent('Secrets');
    expect(within(drawer).getByTestId('sub-nav')).toHaveTextContent('Database');
    for (const old of ['Databases', 'Containers', 'Vault']) expect(within(drawer).queryByText(old)).not.toBeInTheDocument();
  });

  test('the bar above the page names the section the same way', async () => {
    setPhone(true);
    renderShell();
    expect(await screen.findByRole('button', { name: 'Menu' })).toBeInTheDocument();
    expect(screen.getByText('Database', { selector: 'span' })).toBeInTheDocument();
    expect(screen.queryByText('Databases')).not.toBeInTheDocument();
  });

  test('the desktop rail uses the same names', async () => {
    setPhone(false);
    renderShell();
    const rail = await screen.findByTestId('icon-rail');
    // A collapsed rail shows icons; the name is the button's title.
    expect(await within(rail).findByTestId('nav-containers')).toHaveAttribute('title', 'Apps');
    expect(within(rail).getByTestId('nav-database')).toHaveAttribute('title', 'Database');
    expect(within(rail).getByTestId('nav-vault')).toHaveAttribute('title', 'Secrets');
  });
});
