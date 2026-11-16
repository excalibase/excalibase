import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { ProvisionPage } from './ProvisionPage';
import { api } from '../api/client';
import { listMyOrgs } from '../api/orgs';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));
vi.mock('../api/orgs', () => ({ listMyOrgs: vi.fn() }));
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => vi.fn() };
});

const MYSQL_CATALOG = { defaultVersion: '8.4.10', versions: [{ version: '8.4.10' }, { version: '8.4.9' }] };

function renderPage(mysql: boolean) {
  vi.mocked(listMyOrgs).mockResolvedValue([{ id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: 'u1' }]);
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features: { mysql } } } as never);
    if (url === '/postgres/catalog') return Promise.resolve({ data: { majors: [] } } as never);
    if (url === '/mysql/catalog') return Promise.resolve({ data: MYSQL_CATALOG } as never);
    if (url === '/tiers') {
      return Promise.resolve({ data: [{ tier: 'FREE', instances: 1, storageSize: '5Gi', memory: '512Mi', cpu: '0.5', backupEnabled: true }] } as never);
    }
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

// ADR 0041: MySQL projects ship dark behind the mysql feature.
describe('ProvisionPage — MySQL', () => {
  beforeEach(() => vi.clearAllMocks());

  test('the MySQL card stays closed while the feature is off', async () => {
    renderPage(false);
    await screen.findByTestId('engine-MYSQL');
    expect(screen.getByTestId('engine-MYSQL')).toBeDisabled();
    expect(api.get).not.toHaveBeenCalledWith('/mysql/catalog');
  });

  test('with the feature on it creates a MySQL project on the version chosen', async () => {
    renderPage(true);
    const user = userEvent.setup();
    const card = await screen.findByTestId('engine-MYSQL');
    await vi.waitFor(() => expect(card).toBeEnabled());
    // The same card on Kubernetes and on a single host: no operator named.
    expect(card).toHaveTextContent('MySQL 8.4');
    expect(card).not.toHaveTextContent('MOCO');
    await user.click(card);
    expect(await screen.findByTestId('mysql-version-8.4.10')).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByTestId('pg-version-section')).not.toBeInTheDocument();
    // MySQL runs on at least 1Gi, and is backed up like Postgres (EXC-586).
    await vi.waitFor(() => expect(screen.getByTestId('provision-org-plan')).toHaveTextContent('1Gi RAM'));
    expect(screen.getByTestId('provision-org-plan')).toHaveTextContent('Backups');
    await user.click(screen.getByTestId('mysql-version-8.4.9'));
    await user.type(screen.getByLabelText('Project Name'), 'shop');
    await vi.waitFor(() => expect(screen.getByLabelText('Organization')).toHaveValue('org-1'));
    await user.click(screen.getByTestId('provision-submit'));
    await vi.waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(vi.mocked(api.post).mock.calls[0][1]).toEqual({
      projectName: 'shop',
      orgId: 'org-1',
      databaseType: 'MYSQL',
      mysqlVersion: '8.4.9',
    });
  });
});
