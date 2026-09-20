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

describe('ConnectionStrings — PostgreSQL', () => {
  test('renders the URI a Postgres client needs', async () => {
    renderStrings();
    const uri = await screen.findByTestId('conn-postgres-uri');
    expect(uri).toHaveTextContent('postgresql://app:');
    expect(uri).toHaveTextContent('@p-1-postgres-rw.org-1.svc.cluster.local:5432/appdb');
    expect(uri).toHaveTextContent('sslmode=require');
  });

  test('renders a psql command for the same target', async () => {
    renderStrings();
    const psql = await screen.findByTestId('conn-psql');
    expect(psql).toHaveTextContent('psql "postgresql://app:');
    expect(psql).toHaveTextContent('@p-1-postgres-rw.org-1.svc.cluster.local:5432/appdb?sslmode=require"');
  });

  test('keeps the password out of the rendered string until it is revealed', async () => {
    const user = userEvent.setup();
    renderStrings();
    const uri = await screen.findByTestId('conn-postgres-uri');
    expect(uri).not.toHaveTextContent('s3cret');

    await user.click(screen.getByTestId('conn-reveal-password'));
    expect(screen.getByTestId('conn-postgres-uri')).toHaveTextContent('s3cret');
  });

  test('copies the real connection string, password included', async () => {
    const user = userEvent.setup();
    renderStrings();
    await screen.findByTestId('conn-postgres-uri');
    const written = recordClipboard();

    await user.click(screen.getByTestId('conn-copy-postgres-uri'));
    expect(written).toEqual([
      'postgresql://app:s3cret@p-1-postgres-rw.org-1.svc.cluster.local:5432/appdb?sslmode=require',
    ]);
  });

  // EXC-410 owns the public host, port and TLS. Until it supplies them the
  // page shows the in-cluster target and says so, rather than printing a
  // public name nobody has allocated.
  test('shows in-cluster details, labelled, when no external endpoint is supplied', async () => {
    renderStrings();
    expect(await screen.findByTestId('conn-scope')).toHaveTextContent(/in-cluster/i);
  });

  test('prefers the external endpoint once EXC-410 supplies one', async () => {
    renderStrings({
      endpoint: { host: 'p-1.db.excalibase.io', port: 26257, sslMode: 'verify-full' },
    });
    const uri = await screen.findByTestId('conn-postgres-uri');
    expect(uri).toHaveTextContent('p-1.db.excalibase.io:26257');
    expect(uri).toHaveTextContent('sslmode=verify-full');
    expect(screen.getByTestId('conn-scope')).toHaveTextContent(/public/i);
  });
});

describe('ConnectionStrings — DocumentDB', () => {
  test('offers nothing Mongo-shaped for a project without DocumentDB', async () => {
    renderStrings({ documentDb: false });
    await screen.findByTestId('conn-postgres-uri');
    expect(screen.queryByTestId('conn-mongo-section')).not.toBeInTheDocument();
  });

  test('says the Mongo endpoint is not published yet rather than inventing one', async () => {
    renderStrings({ documentDb: true });
    await screen.findByTestId('conn-mongo-section');
    expect(screen.queryByTestId('conn-mongo-uri')).not.toBeInTheDocument();
    expect(screen.getByTestId('conn-mongo-pending')).toBeInTheDocument();
  });

  test('renders a Mongo URI once the endpoint carries a Mongo port', async () => {
    renderStrings({
      documentDb: true,
      endpoint: { host: 'p-1.db.excalibase.io', port: 26257, mongoPort: 27017, sslMode: 'verify-full' },
    });
    const uri = await screen.findByTestId('conn-mongo-uri');
    expect(uri).toHaveTextContent('@p-1.db.excalibase.io:27017/appdb');
    expect(uri).not.toHaveTextContent('s3cret');
  });

  test('copies the Mongo string with its password', async () => {
    const user = userEvent.setup();
    renderStrings({
      documentDb: true,
      endpoint: { host: 'p-1.db.excalibase.io', port: 26257, mongoPort: 27017, sslMode: 'verify-full' },
    });
    await screen.findByTestId('conn-mongo-uri');
    const written = recordClipboard();

    await user.click(screen.getByTestId('conn-copy-mongo-uri'));
    expect(written[0]).toContain('mongodb://app:s3cret@p-1.db.excalibase.io:27017/appdb');
  });
});

describe('ConnectionStrings — certificate authority', () => {
  test('offers no download while no CA has been supplied', async () => {
    renderStrings();
    await screen.findByTestId('conn-postgres-uri');
    expect(screen.queryByTestId('conn-ca-download')).not.toBeInTheDocument();
    expect(screen.getByTestId('conn-ca-pending')).toBeInTheDocument();
  });

  test('offers the CA as a download when the endpoint carries one', async () => {
    renderStrings({
      endpoint: {
        host: 'p-1.db.excalibase.io',
        port: 26257,
        sslMode: 'verify-full',
        caCertPem: '-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n',
      },
    });
    const link = await screen.findByTestId('conn-ca-download');
    expect(link).toHaveAttribute('download', 'p-1-ca.crt');
  });
});
