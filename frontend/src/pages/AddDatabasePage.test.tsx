import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AddDatabasePage } from './AddDatabasePage';
import { DatabaseRequired } from '../components/DatabaseRequired';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const navigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => navigate };
});

const CATALOG = {
  documentDbRef: 'x',
  majors: [
    { major: '14', available: true, documentDb: false },
    { major: '16', available: true, documentDb: true },
  ],
};

function stubProject(project: Record<string, unknown>) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/proj-1') return Promise.resolve({ data: project } as never);
    if (url === '/postgres/catalog') return Promise.resolve({ data: CATALOG } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/project/:projectId/database/add" element={<AddDatabasePage />} />
          <Route path="/project/:projectId" element={<DatabaseRequired />}>
            <Route path="sql" element={<p>sql editor</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const withoutDatabase = { projectId: 'proj-1', status: 'ACTIVE', noDatabase: true, canAddDatabase: true };

describe('AddDatabasePage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  test('adds a PostgreSQL database with the chosen major', async () => {
    const user = userEvent.setup();
    stubProject(withoutDatabase);
    vi.mocked(api.post).mockResolvedValue({ data: { projectId: 'proj-1', status: 'ACTIVE', noDatabase: false } } as never);
    renderAt('/project/proj-1/database/add');

    expect(await screen.findByTestId('add-database-submit')).toBeDisabled();
    await user.click(await screen.findByTestId('pg-version-16'));
    await user.click(screen.getByTestId('add-database-submit'));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision/proj-1/database', {
        databaseType: 'POSTGRESQL',
        postgresVersion: '16',
        documentDb: false,
      }),
    );
    expect(navigate).toHaveBeenCalledWith('/project/proj-1');
  });

  test('adds a DocumentDB database on a capable major', async () => {
    const user = userEvent.setup();
    stubProject(withoutDatabase);
    vi.mocked(api.post).mockResolvedValue({ data: { projectId: 'proj-1', status: 'ACTIVE', noDatabase: false } } as never);
    renderAt('/project/proj-1/database/add');

    await user.click(await screen.findByTestId('add-engine-DOCUMENTDB'));
    await user.click(await screen.findByTestId('pg-version-16'));
    await user.click(screen.getByTestId('add-database-submit'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision/proj-1/database', {
        databaseType: 'POSTGRESQL',
        postgresVersion: '16',
        documentDb: true,
      }),
    );
  });

  test('a failed add names the failure and stays on the page', async () => {
    const user = userEvent.setup();
    stubProject(withoutDatabase);
    vi.mocked(api.post).mockResolvedValue({
      data: { projectId: 'proj-1', status: 'ACTIVE', noDatabase: true, failureReason: 'cluster rejected' },
    } as never);
    renderAt('/project/proj-1/database/add');
    await user.click(await screen.findByTestId('pg-version-16'));
    await user.click(screen.getByTestId('add-database-submit'));
    expect(await screen.findByRole('alert')).toHaveTextContent('cluster rejected');
    expect(navigate).not.toHaveBeenCalled();
  });

  test('a refusal from the server is shown', async () => {
    const user = userEvent.setup();
    stubProject(withoutDatabase);
    vi.mocked(api.post).mockRejectedValue({ response: { data: { error: 'project already has a database' } } });
    renderAt('/project/proj-1/database/add');
    await user.click(await screen.findByTestId('pg-version-16'));
    await user.click(screen.getByTestId('add-database-submit'));
    expect(await screen.findByRole('alert')).toHaveTextContent('project already has a database');
  });

  test('a member the server does not allow is told who can add one', async () => {
    stubProject({ ...withoutDatabase, canAddDatabase: false });
    renderAt('/project/proj-1/database/add');
    expect(await screen.findByTestId('add-database-not-allowed')).toHaveTextContent('Only an org admin or owner');
    expect(screen.queryByTestId('add-database-submit')).not.toBeInTheDocument();
  });

  test('a project that has its database is sent back to it', async () => {
    stubProject({ projectId: 'proj-1', status: 'ACTIVE', noDatabase: false });
    renderAt('/project/proj-1/database/add');
    expect(await screen.findByTestId('add-database-has-one')).toHaveTextContent('already has a database');
  });
});

describe('DatabaseRequired', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  test('a database page of a project without one says so instead of failing', async () => {
    stubProject(withoutDatabase);
    renderAt('/project/proj-1/sql');
    expect(await screen.findByTestId('database-required')).toHaveTextContent('This project has no database');
    expect(screen.queryByText('sql editor')).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: /add database/i })).toHaveAttribute('href', '/project/proj-1/database/add');
  });

  test('a project with a database gets the page', async () => {
    stubProject({ projectId: 'proj-1', status: 'ACTIVE' });
    renderAt('/project/proj-1/sql');
    expect(await screen.findByText('sql editor')).toBeInTheDocument();
  });

  test('a project that cannot be read says so instead of spinning', async () => {
    vi.mocked(api.get).mockRejectedValue({ response: { data: { error: 'project not found' } } });
    renderAt('/project/proj-1/sql');
    expect(await screen.findByRole('alert')).toHaveTextContent('project not found');
  });
});
