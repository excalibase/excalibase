import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ConnectionStrings } from './ConnectionStrings';
import { api } from '../api/client';
import type { ProjectEndpoint } from '../api/projectEndpoint';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

const CREDENTIALS = {
  projectId: 'p-1',
  host: 'p-1-postgres-rw.org-1.svc.cluster.local',
  port: 5432,
  databaseName: 'appdb',
  username: 'app',
  password: 's3cret',
  sslMode: 'require',
  connectionUrl:
    'postgresql://app:s3cret@p-1-postgres-rw.org-1.svc.cluster.local:5432/appdb?sslmode=require',
};

// A project whose public port is open and answering, as GET
// /api/projects/{projectId}/db-endpoint reports it.
const PUBLIC_ENDPOINT: ProjectEndpoint = {
  projectId: 'p-1',
  publicEnabled: true,
  available: true,
  host: 'p-1.db.excalibase.io',
  port: 26257,
  requireTls: true,
  database: 'appdb',
  username: 'app',
  connectionStrings: {
    requireTls: 'postgresql://app@p-1.db.excalibase.io:26257/appdb?sslmode=verify-full',
    allowPlaintext: 'postgresql://app@p-1.db.excalibase.io:26257/appdb?sslmode=prefer',
  },
  caCertificate: '-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n',
  internal: {
    host: 'p-1-postgres-rw.org-1.svc.cluster.local',
    port: 5432,
    connectionString:
      'postgresql://app@p-1-postgres-rw.org-1.svc.cluster.local:5432/appdb?sslmode=prefer',
  },
};

// The same project once its Mongo gateway is up and has created its user.
const WITH_MONGO: ProjectEndpoint = {
  ...PUBLIC_ENDPOINT,
  mongo: {
    available: true,
    port: 27018,
    internal: { host: 'p-1-documentdb.org-1.svc.cluster.local', port: 27017 },
  },
};

function renderStrings(props: { documentDb?: boolean; endpoint?: ProjectEndpoint } = {}) {
  vi.mocked(api.get).mockResolvedValue({ data: CREDENTIALS } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ConnectionStrings projectId="p-1" documentDb={props.documentDb ?? false} endpoint={props.endpoint} />
    </QueryClientProvider>
  );
}

beforeEach(() => vi.clearAllMocks());

// userEvent.setup() installs its own clipboard stub, so the recorder has to go
// in after it or the writes land somewhere nothing reads.
function recordClipboard(): string[] {
  const written: string[] = [];
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: (text: string) => { written.push(text); return Promise.resolve(); } },
  });
  return written;
}

describe('ConnectionStrings — one credential', () => {
  test('shows a single username and a single password, whatever protocols the project answers', async () => {
    renderStrings({ documentDb: true, endpoint: WITH_MONGO });
    await screen.findByTestId('conn-username');
    expect(screen.getAllByTestId('conn-username')).toHaveLength(1);
    expect(screen.getAllByTestId('conn-password')).toHaveLength(1);
    expect(screen.getByTestId('conn-username')).toHaveTextContent('app');
  });

  test('keeps the password masked until it is revealed, in every string at once', async () => {
    const user = userEvent.setup();
    renderStrings({ documentDb: true, endpoint: WITH_MONGO });
    await screen.findByTestId('conn-postgres-public');
    expect(screen.getByTestId('conn-postgres-public')).not.toHaveTextContent('s3cret');
    expect(screen.getByTestId('conn-mongo-public')).not.toHaveTextContent('s3cret');

    await user.click(screen.getByTestId('conn-reveal-password'));
    expect(screen.getByTestId('conn-postgres-public')).toHaveTextContent('s3cret');
    expect(screen.getByTestId('conn-mongo-public')).toHaveTextContent('s3cret');
    expect(screen.getByTestId('conn-password')).toHaveTextContent('s3cret');
  });

  test('copies the real string, password included', async () => {
    const user = userEvent.setup();
    renderStrings({ endpoint: PUBLIC_ENDPOINT });
    await screen.findByTestId('conn-postgres-public');
    const written = recordClipboard();

    await user.click(screen.getByTestId('conn-copy-postgres-public'));
    expect(written).toEqual([
      'postgresql://app:s3cret@p-1.db.excalibase.io:26257/appdb?sslmode=verify-full',
    ]);
  });
});

describe('ConnectionStrings — internal versus public', () => {
  test('shows the internal address even when the project publishes no public port', async () => {
    renderStrings({
      endpoint: { ...PUBLIC_ENDPOINT, publicEnabled: false, available: false, port: 0, caCertificate: '' },
    });
    const internal = await screen.findByTestId('conn-postgres-internal');
    expect(internal).toHaveTextContent('@p-1-postgres-rw.org-1.svc.cluster.local:5432/appdb');
    expect(screen.queryByTestId('conn-postgres-public')).not.toBeInTheDocument();
    expect(screen.getByTestId('conn-postgres-public-absent')).toBeInTheDocument();
  });

  test('shows the internal address alongside the public one, each labelled', async () => {
    renderStrings({ endpoint: PUBLIC_ENDPOINT });
    await screen.findByTestId('conn-postgres-public');
    expect(screen.getByTestId('conn-postgres-internal-label')).toHaveTextContent(/internal/i);
    expect(screen.getByTestId('conn-postgres-public-label')).toHaveTextContent(/public/i);
    expect(screen.getByTestId('conn-postgres-internal')).toHaveTextContent(
      '@p-1-postgres-rw.org-1.svc.cluster.local:5432/appdb'
    );
    expect(screen.getByTestId('conn-postgres-public')).toHaveTextContent('@p-1.db.excalibase.io:26257/appdb');
  });

  test('falls back to the in-cluster credentials when no endpoint has been read', async () => {
    renderStrings();
    const internal = await screen.findByTestId('conn-postgres-internal');
    expect(internal).toHaveTextContent('@p-1-postgres-rw.org-1.svc.cluster.local:5432/appdb');
    expect(screen.queryByTestId('conn-postgres-public')).not.toBeInTheDocument();
  });
});

describe('ConnectionStrings — DocumentDB', () => {
  test('offers nothing Mongo-shaped for a project without DocumentDB', async () => {
    renderStrings({ documentDb: false, endpoint: WITH_MONGO });
    await screen.findByTestId('conn-postgres-public');
    expect(screen.queryByTestId('conn-mongo-section')).not.toBeInTheDocument();
    expect(screen.queryByTestId('conn-mongo-unavailable')).not.toBeInTheDocument();
  });

  test('renders one credential with a Postgres and a Mongo string beside it', async () => {
    renderStrings({ documentDb: true, endpoint: WITH_MONGO });
    await screen.findByTestId('conn-mongo-public');
    expect(screen.getByTestId('conn-postgres-public')).toHaveTextContent('postgresql://app:');
    expect(screen.getByTestId('conn-mongo-public')).toHaveTextContent('mongodb://app:');
    expect(screen.getAllByTestId('conn-password')).toHaveLength(1);
  });

  test('spells TLS the way a Mongo driver reads it', async () => {
    renderStrings({ documentDb: true, endpoint: WITH_MONGO });
    const uri = await screen.findByTestId('conn-mongo-public');
    expect(uri).toHaveTextContent('tls=true');
    expect(uri).not.toHaveTextContent('sslmode');
  });

  test('authenticates the Mongo client against the project database', async () => {
    renderStrings({ documentDb: true, endpoint: WITH_MONGO });
    const uri = await screen.findByTestId('conn-mongo-public');
    expect(uri).toHaveTextContent('@p-1.db.excalibase.io:27018/appdb');
    expect(uri).toHaveTextContent('authSource=appdb');
  });

  test('shows the internal Mongo address too', async () => {
    renderStrings({ documentDb: true, endpoint: WITH_MONGO });
    const internal = await screen.findByTestId('conn-mongo-internal');
    expect(internal).toHaveTextContent('@p-1-documentdb.org-1.svc.cluster.local:27017/appdb');
  });

  test('says the Mongo endpoint is not answering yet while Postgres already is', async () => {
    renderStrings({
      documentDb: true,
      endpoint: { ...PUBLIC_ENDPOINT, mongo: { available: false } },
    });
    await screen.findByTestId('conn-mongo-section');
    expect(screen.getByTestId('conn-postgres-public')).toBeInTheDocument();
    expect(screen.queryByTestId('conn-mongo-public')).not.toBeInTheDocument();
    expect(screen.getByTestId('conn-mongo-unavailable')).toHaveTextContent(/not answering yet/i);
  });

  test('says so rather than inventing a Mongo endpoint the API has not reported', async () => {
    renderStrings({ documentDb: true, endpoint: PUBLIC_ENDPOINT });
    await screen.findByTestId('conn-mongo-section');
    expect(screen.queryByTestId('conn-mongo-public')).not.toBeInTheDocument();
    expect(screen.getByTestId('conn-mongo-unavailable')).toBeInTheDocument();
  });
});

describe('ConnectionStrings — certificate authority', () => {
  test('offers the CA beside both the Postgres and the Mongo string', async () => {
    renderStrings({ documentDb: true, endpoint: WITH_MONGO });
    const links = await screen.findAllByTestId('conn-ca-download');
    expect(links).toHaveLength(2);
    expect(links[0]).toHaveAttribute('download', 'p-1-ca.crt');
  });

  test('offers no download while the endpoint publishes no CA', async () => {
    renderStrings();
    await screen.findByTestId('conn-postgres-internal');
    expect(screen.queryByTestId('conn-ca-download')).not.toBeInTheDocument();
  });
});
