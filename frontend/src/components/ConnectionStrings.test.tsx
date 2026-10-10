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

  // EXC-530: Require TLS off opens plaintext for Postgres only; the gateway
  // takes TLS alone, so the Mongo string never offers plaintext.
  test('asks for TLS on Mongo even when Postgres allows plaintext', async () => {
    renderStrings({ documentDb: true, endpoint: { ...WITH_MONGO, requireTls: false } });
    const uri = await screen.findByTestId('conn-mongo-public');
    expect(uri).toHaveTextContent('tls=true');
    expect(uri).not.toHaveTextContent('tls=false');
    expect(screen.getByTestId('conn-postgres-public')).toHaveTextContent('sslmode=prefer');
  });

  test('authenticates the way the gateway was proven to accept', async () => {
    renderStrings({ documentDb: true, endpoint: WITH_MONGO });
    const uri = await screen.findByTestId('conn-mongo-public');
    expect(uri).toHaveTextContent('@p-1.db.excalibase.io:27018/?tls=true&authMechanism=SCRAM-SHA-256&directConnection=true');
    expect(uri).not.toHaveTextContent('authSource');
  });

  // The gateway is one server, not a replica set: without directConnection a
  // driver follows the member address the gateway reports and cannot reach it.
  test('connects directly to the one gateway, internal and public', async () => {
    renderStrings({ documentDb: true, endpoint: WITH_MONGO });
    expect(await screen.findByTestId('conn-mongo-public')).toHaveTextContent('directConnection=true');
    expect(screen.getByTestId('conn-mongo-internal')).toHaveTextContent('directConnection=true');
  });

  test('shows the internal Mongo address too, over TLS', async () => {
    renderStrings({ documentDb: true, endpoint: WITH_MONGO });
    const internal = await screen.findByTestId('conn-mongo-internal');
    expect(internal).toHaveTextContent('@p-1-documentdb.org-1.svc.cluster.local:27017/?tls=true');
  });

  test('shows the internal Mongo address of a project that publishes no public port', async () => {
    renderStrings({
      documentDb: true,
      endpoint: {
        ...PUBLIC_ENDPOINT,
        publicEnabled: false,
        available: false,
        port: 0,
        mongo: { available: false, internal: { host: 'p-1-postgres-rw.org-1.svc.cluster.local', port: 10260 } },
      },
    });
    const internal = await screen.findByTestId('conn-mongo-internal');
    expect(internal).toHaveTextContent('@p-1-postgres-rw.org-1.svc.cluster.local:10260/');
    expect(screen.queryByTestId('conn-mongo-unavailable')).not.toBeInTheDocument();
  });

  test('says the Mongo endpoint is not answering yet while Postgres already is', async () => {
    renderStrings({
      documentDb: true,
      endpoint: {
        ...PUBLIC_ENDPOINT,
        mongo: { available: false, port: 27018, internal: { host: 'p-1-postgres-rw.org-1.svc.cluster.local', port: 10260 } },
      },
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

  // In-cluster logins need TLS too, so a private project still gets its CA.
  test('offers the CA beside the internal strings of a project with no public port', async () => {
    renderStrings({
      documentDb: true,
      endpoint: { ...WITH_MONGO, publicEnabled: false, available: false, port: 0, mongo: { available: false, internal: WITH_MONGO.mongo!.internal } },
    });
    await screen.findByTestId('conn-mongo-internal');
    expect(screen.queryByTestId('conn-postgres-public')).not.toBeInTheDocument();
    expect(screen.getAllByTestId('conn-ca-download')).toHaveLength(2);
  });

  test('offers no download while the endpoint publishes no CA', async () => {
    renderStrings();
    await screen.findByTestId('conn-postgres-internal');
    expect(screen.queryByTestId('conn-ca-download')).not.toBeInTheDocument();
  });
});

// EXC-576: a single host publishes its ports on its own 127.0.0.1. Postgres
// there serves no TLS; the Mongo gateway does, with a CA of its own.
const SINGLE_HOST: ProjectEndpoint = {
  projectId: 'p-1',
  singleHost: true,
  publicOffered: true,
  publicEnabled: true,
  available: true,
  host: '127.0.0.1',
  port: 32801,
  requireTls: false,
  database: 'appdb',
  username: 'postgres',
  connectionStrings: { requireTls: '', allowPlaintext: 'postgresql://postgres@127.0.0.1:32801/appdb?sslmode=disable' },
  caCertificate: '-----BEGIN CERTIFICATE-----\nGW\n-----END CERTIFICATE-----\n',
  internal: { host: 'excalibase-p-1-postgres', port: 5432, connectionString: '' },
  mongo: { available: true, port: 32802, internal: { host: 'excalibase-p-1-postgres', port: 10260 } },
};

describe('ConnectionStrings — single host', () => {
  test('gives the host loopback strings and says how to reach them from elsewhere', async () => {
    renderStrings({ documentDb: true, endpoint: SINGLE_HOST });
    expect(await screen.findByTestId('conn-postgres-public')).toHaveTextContent('@127.0.0.1:32801/appdb?sslmode=disable');
    expect(screen.getByTestId('conn-postgres-public-label')).toHaveTextContent('On the host');
    expect(screen.getByTestId('conn-postgres-internal')).toHaveTextContent('@excalibase-p-1-postgres:5432/appdb?sslmode=disable');
    expect(screen.getByTestId('conn-mongo-public')).toHaveTextContent('@127.0.0.1:32802/?tls=true');
    expect(screen.getAllByTestId('conn-ssh-tunnel')[0]).toHaveTextContent('ssh -N -L 32801:127.0.0.1:32801');
  });

  test('offers the gateway CA beside the Mongo string only, since Postgres there has no TLS', async () => {
    renderStrings({ documentDb: true, endpoint: SINGLE_HOST });
    const postgres = await screen.findByTestId('conn-postgres-section');
    expect(postgres.querySelector('[data-testid="conn-ca-download"]')).toBeNull();
    const mongo = screen.getByTestId('conn-mongo-section');
    expect(mongo.querySelector('[data-testid="conn-ca-download"]')).not.toBeNull();
  });
});
