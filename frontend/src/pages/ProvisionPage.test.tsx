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

const CATALOG = {
  documentDbRef: 'v0.117-0',
  majors: [
    {
      major: '14',
      available: true,
      documentDb: false,
      documentDbUnavailableReason:
        'DocumentDB is not available on PostgreSQL 14. The extension is built only for PostgreSQL 15, 16, 17.',
    },
    { major: '15', available: true, documentDb: true },
    { major: '16', available: true, documentDb: true },
    { major: '17', available: false, documentDb: true },
  ],
};

function renderPage() {
  vi.mocked(listMyOrgs).mockResolvedValue([
    { id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: 'u1' },
  ]);
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/postgres/catalog') return Promise.resolve({ data: CATALOG } as never);
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
    </QueryClientProvider>
  );
}

describe('ProvisionPage — PostgreSQL version', () => {
  beforeEach(() => vi.clearAllMocks());

  test('offers exactly the majors the catalogue lists', async () => {
    renderPage();
    await screen.findByTestId('pg-version-14');
    for (const major of ['14', '15', '16', '17']) {
      expect(screen.getByTestId(`pg-version-${major}`)).toBeInTheDocument();
    }
    expect(screen.queryByTestId('pg-version-18')).not.toBeInTheDocument();
    expect(screen.queryByTestId('pg-version-13')).not.toBeInTheDocument();
  });

  // No implicit default: the customer has to say which major their data lives
  // on, because nobody else can make that choice for them.
  test('pre-selects no version and refuses to submit until one is chosen', async () => {
    renderPage();
    await screen.findByTestId('pg-version-14');

    for (const major of ['14', '15', '16', '17']) {
      expect(screen.getByTestId(`pg-version-${major}`)).toHaveAttribute('aria-pressed', 'false');
    }
    expect(screen.getByTestId('provision-submit')).toBeDisabled();
  });

  test('a major with no published image cannot be chosen', async () => {
    renderPage();
    await screen.findByTestId('pg-version-17');
    expect(screen.getByTestId('pg-version-17')).toBeDisabled();
  });

  test('sends the chosen major with the provisioning request', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByTestId('pg-version-16');

    await user.type(screen.getByLabelText('Project Name'), 'my-db');
    await user.click(screen.getByTestId('pg-version-16'));
    await user.click(screen.getByTestId('provision-submit'));

    await waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(vi.mocked(api.post).mock.calls[0][1]).toMatchObject({ postgresVersion: '16' });
  });
});

describe('ProvisionPage — DocumentDB', () => {
  beforeEach(() => vi.clearAllMocks());

  test('is closed on 14 and says why, rather than hiding or failing later', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByTestId('pg-version-14');

    await user.click(screen.getByTestId('pg-version-14'));

    expect(screen.getByTestId('documentdb-toggle')).toBeDisabled();
    expect(screen.getByTestId('documentdb-reason')).toHaveTextContent(
      CATALOG.majors[0].documentDbUnavailableReason as string
    );
  });

  test('is available from 15 upwards', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByTestId('pg-version-15');

    await user.click(screen.getByTestId('pg-version-15'));
    expect(screen.getByTestId('documentdb-toggle')).toBeEnabled();

    await user.click(screen.getByTestId('pg-version-16'));
    expect(screen.getByTestId('documentdb-toggle')).toBeEnabled();
  });

  test('a DocumentDB choice does not survive a move to a major that cannot carry it', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByTestId('pg-version-15');

    await user.type(screen.getByLabelText('Project Name'), 'my-db');
    await user.click(screen.getByTestId('pg-version-15'));
    await user.click(screen.getByTestId('documentdb-toggle'));
    expect(screen.getByTestId('documentdb-toggle')).toBeChecked();

    await user.click(screen.getByTestId('pg-version-14'));
    await user.click(screen.getByTestId('provision-submit'));

    await waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(vi.mocked(api.post).mock.calls[0][1]).toMatchObject({ postgresVersion: '14', documentDb: false });
  });

  test('cannot be chosen before a version is', async () => {
    renderPage();
    await screen.findByTestId('pg-version-14');
    expect(screen.getByTestId('documentdb-toggle')).toBeDisabled();
  });
});
