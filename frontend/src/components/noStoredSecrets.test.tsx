import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { api } from '../api/client';
import { ConnectionStrings } from './ConnectionStrings';
import { CredentialsViewer } from './CredentialsViewer';
import { InstanceDetailPage } from '../pages/InstanceDetailPage';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));
vi.mock('../hooks/useProjectRole', () => ({ useProjectRole: () => ({ canAdmin: true, canDevelop: true, role: 'admin', isLoading: false }) }));

const INTERNAL_HOST = 'p-1-postgres-rw.org-1.svc.cluster.local';
const CREDENTIALS = {
  projectId: 'p-1', host: INTERNAL_HOST, port: 5432, databaseName: 'appdb', username: 'app', sslMode: 'require',
  connectionUrl: `postgresql://app:<password>@${INTERNAL_HOST}:5432/appdb?sslmode=require`,
  readerUsername: 'excalibase_reader',
};
const PROJECT = {
  projectId: 'p-1', orgId: 'org-secret-id', databaseType: 'POSTGRESQL', tier: 'FREE', status: 'ACTIVE', currentStage: 'COMPLETED',
  namespace: 'org-1-p-1', host: INTERNAL_HOST, port: 5432, databaseName: 'appdb',
  backupEnabled: true, backupSchedule: '0 2 * * *', metricsEndpoint: `http://${INTERNAL_HOST}:9187/metrics`,
  createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z',
};
const PUBLIC_ENDPOINT = {
  projectId: 'p-1', publicEnabled: true, available: true, host: 'p-1.db.excalibase.io', port: 26257, requireTls: true,
  database: 'appdb', username: 'app', connectionStrings: { requireTls: '', allowPlaintext: '' },
  caCertificate: '-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n',
};
const PRIVATE_ENDPOINT = { ...PUBLIC_ENDPOINT, publicEnabled: false, available: false, port: 0 };

function mockApi({ endpoint = PRIVATE_ENDPOINT, apps = [] as unknown[] } = {}) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/config') return { data: { deploymentMode: 'cloud', appHosting: true, features: { onetimecredentials: true } } } as never;
    if (url === '/provision/p-1') return { data: PROJECT } as never;
    if (url === '/provision/p-1/credentials') return { data: CREDENTIALS } as never;
    if (url === '/projects/p-1/db-endpoint') return { data: endpoint } as never;
    if (url === '/projects/p-1/apps/') return { data: apps } as never;
    return { data: [] } as never;
  });
}

function wrap(ui: React.ReactElement) {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <MemoryRouter initialEntries={['/project/p-1/database/overview']}>
        <Routes><Route path="/project/:projectId/database/overview" element={ui} /></Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function expectNoLeaks(text: string) {
  expect(text).not.toContain('svc.cluster.local');
  expect(text).not.toContain(':9187/metrics');
  expect(text).not.toContain('org-secret-id');
  expect(text).not.toContain('0 2 * * *');
  expect(text).not.toMatch(/s3cret|reader-pass/);
}

describe('credentials never display a stored password', () => {
  beforeEach(() => vi.clearAllMocks());

  test('the connection section shows <password>, copies it, and offers a reset', async () => {
    mockApi({ endpoint: PUBLIC_ENDPOINT as never });
    wrap(<ConnectionStrings projectId="p-1" documentDb={false} endpoint={PUBLIC_ENDPOINT as never} />);
    const row = await screen.findByTestId('conn-postgres-public');
    expect(row).toHaveTextContent('postgresql://app:<password>@p-1.db.excalibase.io:26257/appdb');
    expect(screen.queryByTestId('conn-reveal-password')).not.toBeInTheDocument();
    expect(screen.queryByTestId('conn-password')).not.toBeInTheDocument();
    expect(screen.queryByTestId('conn-reader-password')).not.toBeInTheDocument();
    expect(screen.getByTestId('conn-reader-username')).toHaveTextContent('excalibase_reader');
    expect(await screen.findByRole('button', { name: /reset password/i })).toBeInTheDocument();
    expect(screen.getByTestId('conn-password-note')).toHaveTextContent(/reset password to get new credentials/i);
    expectNoLeaks(document.body.textContent ?? '');
  });

  test('a private project says it is not reachable and prints no internal host', async () => {
    mockApi();
    wrap(<ConnectionStrings projectId="p-1" documentDb={false} endpoint={PRIVATE_ENDPOINT as never} />);
    const note = await screen.findByTestId('conn-postgres-public-absent');
    expect(note).toHaveTextContent(/not reachable from the internet/i);
    expect(note).toHaveTextContent(/public access/i);
    expect(screen.queryByTestId('conn-postgres-internal')).not.toBeInTheDocument();
    expectNoLeaks(document.body.textContent ?? '');
  });

  test('a project that hosts apps shows the DATABASE_URL note and no address', async () => {
    mockApi({ apps: [{ id: 'a1', name: 'web' }] });
    wrap(<ConnectionStrings projectId="p-1" documentDb={false} endpoint={PRIVATE_ENDPOINT as never} />);
    expect(await screen.findByTestId('apps-database-url-note')).toHaveTextContent(/DATABASE_URL injected automatically/);
    expect(screen.queryByTestId('conn-postgres-internal')).not.toBeInTheDocument();
    expectNoLeaks(document.body.textContent ?? '');
  });

  test('copy puts the <password> placeholder on the clipboard', async () => {
    const user = userEvent.setup();
    mockApi({ endpoint: PUBLIC_ENDPOINT as never });
    wrap(<ConnectionStrings projectId="p-1" documentDb={false} endpoint={PUBLIC_ENDPOINT as never} />);
    await screen.findByTestId('conn-postgres-public');
    const written: string[] = [];
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: (text: string) => { written.push(text); return Promise.resolve(); } },
    });
    await user.click(screen.getByTestId('conn-copy-postgres-public'));
    expect(written).toEqual(['postgresql://app:<password>@p-1.db.excalibase.io:26257/appdb?sslmode=verify-full']);
  });

  test('the credentials tab shows the placeholder, no password row and no internal host', async () => {
    mockApi({ endpoint: PUBLIC_ENDPOINT as never });
    wrap(<CredentialsViewer projectId="p-1" />);
    await screen.findByText('Connection Credentials');
    expect(await screen.findByText(/:<password>@p-1\.db\.excalibase\.io:26257/)).toBeInTheDocument();
    expect(screen.queryByText('Password:')).not.toBeInTheDocument();
    expectNoLeaks(document.body.textContent ?? '');
  });

  test('the overview shows no org id, internal host, metrics URL or raw cron', async () => {
    mockApi();
    wrap(<InstanceDetailPage />);
    await screen.findByText('Nightly backups at 02:00 UTC');
    expectNoLeaks(document.body.textContent ?? '');
    await userEvent.click(screen.getByRole('button', { name: /credentials/i }));
    await screen.findByText('Connection Credentials');
    expectNoLeaks(document.body.textContent ?? '');
  });
});
