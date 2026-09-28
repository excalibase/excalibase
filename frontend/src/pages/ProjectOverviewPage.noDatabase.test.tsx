import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ProjectOverviewPage } from './ProjectOverviewPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }));

const noDatabaseProject = (overrides: Record<string, unknown> = {}) => ({
  projectId: 'proj-1',
  orgId: 'org-1',
  databaseType: '',
  tier: 'FREE',
  status: 'ACTIVE',
  currentStage: 'COMPLETED',
  namespace: 'org-1-proj-1',
  noDatabase: true,
  canAddDatabase: true,
  ...overrides,
});

function renderPage(project: Record<string, unknown>) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    const answer = (value: unknown) => Promise.resolve({ data: value } as never);
    if (url === '/config') return answer({ deploymentMode: 'cloud', appHosting: true });
    if (url === '/provision/proj-1') return answer(project);
    if (url === '/projects/proj-1/apps/') return answer([]);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1']}>
        <Routes>
          <Route path="/project/:projectId" element={<ProjectOverviewPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// EXC-426: a project created without a database says so on its overview.
describe('ProjectOverviewPage — no database', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
  });

  test('shows "No database" and offers an admin the add', async () => {
    renderPage(noDatabaseProject());
    const card = await screen.findByTestId('service-database');
    expect(await within(card).findByTestId('service-database-status')).toHaveTextContent('No database');
    expect(within(card).getByRole('link', { name: /add database/i })).toHaveAttribute(
      'href',
      '/project/proj-1/database/add',
    );
    expect(within(card).queryByRole('link', { name: /open database/i })).not.toBeInTheDocument();
    // No public-port read for a database that does not exist.
    expect(api.get).not.toHaveBeenCalledWith('/projects/proj-1/db-endpoint');
  });

  test('a member the server does not allow sees the state and who can add one', async () => {
    renderPage(noDatabaseProject({ canAddDatabase: false }));
    const card = await screen.findByTestId('service-database');
    await within(card).findByTestId('service-database-status');
    expect(within(card).queryByRole('link', { name: /add database/i })).not.toBeInTheDocument();
    expect(within(card).getByTestId('service-database-empty')).toHaveTextContent(
      'Only an org admin or owner can add one',
    );
  });

  test('names the last failed add', async () => {
    renderPage(noDatabaseProject({ failureReason: 'cluster rejected' }));
    const card = await screen.findByTestId('service-database');
    expect(await within(card).findByText(/cluster rejected/)).toBeInTheDocument();
  });
});
