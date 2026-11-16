import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { ProvisionPage } from './ProvisionPage';
import { api } from '../api/client';
import { listMyOrgs } from '../api/orgs';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));
vi.mock('../api/orgs', () => ({ listMyOrgs: vi.fn() }));

const navigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => navigate };
});

function renderPage() {
  vi.mocked(listMyOrgs).mockResolvedValue([{ id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: 'u1' }]);
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/postgres/catalog') {
      return Promise.resolve({ data: { documentDbRef: 'x', majors: [{ major: '16', available: true, documentDb: true }] } } as never);
    }
    if (url === '/tiers') return Promise.resolve({ data: [] } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  vi.mocked(api.post).mockResolvedValue({ data: { projectId: 'p-1' } } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <ProvisionPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// EXC-426: a project does not have to start with a database.
describe('ProvisionPage — no database', () => {
  beforeEach(() => vi.clearAllMocks());

  test('creates a project with no database settings at all', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByTestId('engine-NONE');
    await user.type(screen.getByLabelText('Project Name'), 'apps');
    await waitFor(() => expect(screen.getByLabelText('Organization')).toHaveValue('org-1'));

    await user.click(screen.getByTestId('engine-NONE'));
    expect(screen.queryByTestId('pg-version-16')).not.toBeInTheDocument();
    expect(screen.getByTestId('provision-submit')).toBeEnabled();
    expect(screen.getByTestId('provision-submit')).toHaveTextContent('Create project');

    await user.click(screen.getByTestId('provision-submit'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision', { projectName: 'apps', orgId: 'org-1', noDatabase: true }, expect.anything()),
    );
    expect(navigate).toHaveBeenCalledWith('/project/p-1');
  });

  test('says a database can be added later', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByTestId('engine-NONE'));
    expect(screen.getByTestId('no-database-note')).toHaveTextContent('add a database later');
  });
});
