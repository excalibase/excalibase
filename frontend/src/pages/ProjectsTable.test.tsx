import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { InstancesPage } from './InstancesPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';
import { useOrgSelection } from '../stores/org-selection';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const ORGS = [
  { id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: 'u1', role: 'owner' },
  { id: 'org-other', name: 'Not Mine', slug: 'nm', tier: 'FREE', ownerId: 'u9' },
];

function project(projectId: string, extra: object = {}) {
  return { projectId, orgId: 'org-1', databaseType: 'POSTGRESQL', tier: 'FREE', namespace: 'ns', status: 'ACTIVE', currentStage: 'COMPLETED', createdAt: '2026-10-01T00:00:00Z', ...extra };
}

function mount(flag: boolean, projects: unknown, failProjects = false) {
  useAuthStore.setState({ user: { id: 'u1', username: 'dev', email: 'dev@example.test', role: 'user' }, isAuthenticated: true });
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features: { studio_v2: flag } } }) as never;
    if (url === '/orgs') return Promise.resolve({ data: ORGS }) as never;
    if (url === '/provision') return failProjects ? Promise.reject({ response: { status: 403 } }) : Promise.resolve({ data: projects }) as never;
    return Promise.resolve({ data: [] }) as never;
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter><InstancesPage /></MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('Projects list', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useOrgSelection.setState({ orgId: null });
  });

  test('flag off keeps the current page', async () => {
    mount(false, [project('alpha')]);
    expect(await screen.findByText('1 instance')).toBeInTheDocument();
    expect(screen.queryByRole('searchbox')).not.toBeInTheDocument();
  });

  test('flag on shows the shared table with filter and range', async () => {
    mount(true, [project('alpha'), project('beta', { tier: 'STANDARD' })]);
    expect(await screen.findByRole('searchbox', { name: /filter projects/i })).toBeInTheDocument();
    expect(await screen.findByText('1–2 of 2')).toBeInTheDocument();
    await userEvent.type(screen.getByRole('searchbox'), 'beta');
    const rows = screen.getAllByRole('row').slice(1);
    expect(rows).toHaveLength(1);
    expect(within(rows[0]).getByRole('link', { name: /beta/ })).toHaveAttribute('href', '/project/beta');
  });

  test('only projects of my orgs are listed', async () => {
    mount(true, [project('alpha'), project('foreign', { orgId: 'org-other' })]);
    expect(await screen.findByRole('link', { name: /alpha/ })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /foreign/ })).not.toBeInTheDocument();
  });

  test('a failed load is an error, not "no projects"', async () => {
    mount(true, [], true);
    expect(await screen.findByRole('alert')).toHaveTextContent(/do not have access/i);
    expect(screen.queryByText(/no projects yet/i)).not.toBeInTheDocument();
  });

  test('no projects shows the empty state with a way to create one', async () => {
    mount(true, []);
    expect(await screen.findByText(/no projects yet/i)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /new project/i })).toHaveAttribute('href', '/provision');
  });
});
