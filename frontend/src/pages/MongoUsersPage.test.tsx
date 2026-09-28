import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { MongoUsersPage } from './MongoUsersPage';
import { SubNav } from '../components/layout/SubNav';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

const usersPath = '/provision/proj-doc/documentdb/users';

function mockServer(documentDb = true) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    const reply = (data: unknown) => Promise.resolve({ data } as never);
    if (url === '/provision/proj-doc') return reply({ projectId: 'proj-doc', documentDb });
    if (url === usersPath) {
      return reply({
        users: [{ username: 'reporting', role: 'read', createdAt: '2026-09-28T01:00:00Z' }],
        limit: 100,
      });
    }
    if (url === '/projects/proj-doc/db-endpoint') {
      return reply({
        internal: { host: 'proj-doc-documentdb.ns.svc', port: 5432, mongoPort: 10260 },
        publicEnabled: false,
      });
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  vi.mocked(api.delete).mockResolvedValue({ data: null } as never);
}

function renderAt(element: React.ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-doc/database/mongo-users']}>
        <Routes>
          <Route path="/project/:projectId/database/mongo-users" element={element} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('MongoUsersPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
  });

  test('lists users by name and role, never a password', async () => {
    mockServer();
    renderAt(<MongoUsersPage />);
    const row = await screen.findByTestId('mongo-user-reporting');
    expect(row).toHaveTextContent('reporting');
    expect(row).toHaveTextContent(/read-only/i);
    expect(screen.getByText(/1 of 100/)).toBeInTheDocument();
  });

  test('a new user is shown once with a connection string', async () => {
    mockServer();
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({
      data: { username: 'writer', role: 'readWrite', password: 'pw-shown-once' },
    } as never);
    renderAt(<MongoUsersPage />);
    await screen.findByTestId('mongo-user-reporting');

    await u.type(screen.getByLabelText('User name'), 'writer');
    await u.selectOptions(screen.getByLabelText('Role'), 'readWrite');
    await u.click(screen.getByRole('button', { name: /create user/i }));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(usersPath, { username: 'writer', role: 'readWrite' }),
    );
    const shown = await screen.findByTestId('new-mongo-user');
    expect(shown).toHaveTextContent('pw-shown-once');
    expect(shown).toHaveTextContent(
      'mongodb://writer:pw-shown-once@proj-doc-documentdb.ns.svc:10260/?tls=true&authMechanism=SCRAM-SHA-256',
    );
    await u.click(within(shown).getByRole('button', { name: /i have saved it/i }));
    expect(screen.queryByText(/pw-shown-once/)).not.toBeInTheDocument();
  });

  test('a name the browser can already tell is invalid is refused before sending', async () => {
    mockServer();
    const u = userEvent.setup();
    renderAt(<MongoUsersPage />);
    await screen.findByTestId('mongo-user-reporting');

    await u.type(screen.getByLabelText('User name'), 'Bad-Name');
    await u.click(screen.getByRole('button', { name: /create user/i }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/lowercase/i);
    expect(api.post).not.toHaveBeenCalled();
  });

  test('the server refusing a user is shown in its words', async () => {
    mockServer();
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({
      response: { data: { error: 'a user with this name already exists in the project' } },
    });
    renderAt(<MongoUsersPage />);
    await screen.findByTestId('mongo-user-reporting');

    await u.type(screen.getByLabelText('User name'), 'reporting');
    await u.click(screen.getByRole('button', { name: /create user/i }));

    expect(await screen.findByRole('alert')).toHaveTextContent('already exists');
  });

  test('rotating shows the new password once; deleting asks first', async () => {
    mockServer();
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({
      data: { username: 'reporting', role: 'read', password: 'pw-rotated' },
    } as never);
    renderAt(<MongoUsersPage />);
    const row = await screen.findByTestId('mongo-user-reporting');

    await u.click(within(row).getByRole('button', { name: /rotate/i }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith(`${usersPath}/reporting/rotate`));
    expect(await screen.findByTestId('new-mongo-user')).toHaveTextContent('pw-rotated');

    await u.click(within(row).getByRole('button', { name: /delete/i }));
    expect(window.confirm).toHaveBeenCalled();
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith(`${usersPath}/reporting`));
  });

  test('a project without DocumentDB is told so and nothing is listed', async () => {
    mockServer(false);
    renderAt(<MongoUsersPage />);
    expect(await screen.findByTestId('mongo-users-unavailable')).toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith(usersPath);
  });
});

describe('MongoUsersPage failures and the public address', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
  });

  test('a failed rotate or delete says why, and a declined confirm sends nothing', async () => {
    mockServer();
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({
      response: { data: { error: 'another operation on this project is running' } },
    });
    vi.mocked(api.delete).mockRejectedValue({});
    renderAt(<MongoUsersPage />);
    const row = await screen.findByTestId('mongo-user-reporting');

    await u.click(within(row).getByRole('button', { name: /rotate/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent('another operation');

    await u.click(within(row).getByRole('button', { name: /delete/i }));
    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent('Could not delete the user'),
    );

    vi.mocked(window.confirm).mockReturnValue(false);
    vi.mocked(api.post).mockClear();
    await u.click(within(row).getByRole('button', { name: /rotate/i }));
    expect(api.post).not.toHaveBeenCalled();
  });

  test('users that cannot be loaded say so', async () => {
    mockServer();
    const base = vi.mocked(api.get).getMockImplementation()!;
    vi.mocked(api.get).mockImplementation((url: string) =>
      url === usersPath
        ? Promise.reject({ response: { data: { error: 'vault sealed' } } })
        : base(url),
    );
    renderAt(<MongoUsersPage />);
    expect(await screen.findByText('vault sealed')).toBeInTheDocument();
  });

  test('a public connection string is shown only while the public Mongo port answers', async () => {
    mockServer();
    const base = vi.mocked(api.get).getMockImplementation()!;
    vi.mocked(api.get).mockImplementation((url: string) =>
      url === '/projects/proj-doc/db-endpoint'
        ? Promise.resolve({
            data: {
              publicEnabled: true,
              available: true,
              host: 'db.example.test',
              port: 30001,
              requireTls: true,
              mongoPort: 30002,
              mongoAvailable: true,
              internal: { host: 'proj-doc-documentdb.ns.svc', port: 5432, mongoPort: 10260 },
            },
          } as never)
        : base(url),
    );
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({
      data: { username: 'svc', role: 'read', password: 'pw-public' },
    } as never);
    renderAt(<MongoUsersPage />);
    await screen.findByTestId('mongo-user-reporting');
    await u.type(screen.getByLabelText('User name'), 'svc');
    await u.click(screen.getByRole('button', { name: /create user/i }));
    const shown = await screen.findByTestId('new-mongo-user');
    expect(shown).toHaveTextContent(
      'mongodb://svc:pw-public@db.example.test:30002/?tls=true&authMechanism=SCRAM-SHA-256',
    );
  });
});

describe('Database sub-navigation', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows Mongo Users only for a DocumentDB project', async () => {
    mockServer(true);
    const { unmount } = renderAt(<SubNav sectionKey="database" />);
    expect(await screen.findByRole('link', { name: /mongo users/i })).toBeInTheDocument();
    unmount();

    mockServer(false);
    renderAt(<SubNav sectionKey="database" />);
    await screen.findByRole('link', { name: /tables/i });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/provision/proj-doc'));
    expect(screen.queryByRole('link', { name: /mongo users/i })).toBeNull();
  });
});
