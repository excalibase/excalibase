import { useState } from 'react';
import { Copy, Check, Eye, EyeOff, Loader2, Download } from 'lucide-react';
import { useCredentials } from '../hooks/useProvisioning';
import type { ProjectEndpoint } from '../api/projectEndpoint';
import type { CredentialsResponse } from '../types';

interface ConnectionStringsProps {
  readonly projectId: string;
  // Whether this project's image carries DocumentDB, so Mongo clients have
  // something to connect to.
  readonly documentDb: boolean;
  // The public endpoint, once EXC-410 supplies one. See api/projectEndpoint.
  readonly endpoint?: ProjectEndpoint;
}

// target is where the customer actually connects. With no external endpoint
// published it is the in-cluster service, and it is labelled as such — a page
// that printed a public-looking host nobody had allocated would be worse than
// one that admits what it knows.
interface Target {
  host: string;
  port: number;
  sslMode: string;
  mongoPort?: number;
  scope: 'public' | 'in-cluster';
}

function resolveTarget(credentials: CredentialsResponse, endpoint?: ProjectEndpoint): Target {
  if (endpoint) {
    return {
      host: endpoint.host,
      port: endpoint.port,
      sslMode: endpoint.sslMode ?? credentials.sslMode ?? 'require',
      mongoPort: endpoint.mongoPort,
      scope: 'public',
    };
  }
  return {
    host: credentials.host,
    port: credentials.port,
    sslMode: credentials.sslMode ?? 'require',
    scope: 'in-cluster',
  };
}

const MASKED = '••••••••';

function postgresUri(credentials: CredentialsResponse, target: Target, password: string): string {
  return `postgresql://${credentials.username}:${password}@${target.host}:${target.port}/${credentials.databaseName}?sslmode=${target.sslMode}`;
}

// A Mongo client authenticates against the database it connects to, so the
// database name carries both the path and authSource.
function mongoUri(credentials: CredentialsResponse, target: Target, password: string, port: number): string {
  return `mongodb://${credentials.username}:${password}@${target.host}:${port}/${credentials.databaseName}?authSource=${credentials.databaseName}&tls=true`;
}

interface StringRowProps {
  readonly testId: string;
  readonly label: string;
  readonly shown: string;
  readonly copied: string;
}

// StringRow shows a connection string with the password masked and copies the
// real one. What goes to the clipboard is what a client needs; what goes on
// screen is what is safe over someone's shoulder.
function StringRow({ testId, label, shown, copied }: StringRowProps) {
  const [justCopied, setJustCopied] = useState(false);
  const onCopy = () => {
    navigator.clipboard.writeText(copied);
    setJustCopied(true);
    setTimeout(() => setJustCopied(false), 1500);
  };
  return (
    <div>
      <div className="text-xs text-text-tertiary mb-1">{label}</div>
      <div className="flex items-center gap-2">
        <code
          data-testid={testId}
          className="flex-1 bg-bg-tertiary border border-border-primary rounded px-3 py-2 text-xs font-mono text-text-primary break-all"
        >
          {shown}
        </code>
        <button
          type="button"
          onClick={onCopy}
          aria-label={`Copy ${label}`}
          data-testid={`conn-copy-${testId.replace('conn-', '')}`}
          className="p-2 rounded border border-border-primary text-text-secondary hover:text-text-primary hover:border-text-secondary transition-colors"
        >
          {justCopied ? <Check className="w-4 h-4 text-green-400" /> : <Copy className="w-4 h-4" />}
        </button>
      </div>
    </div>
  );
}

function CertificateAuthority({ projectId, endpoint }: { readonly projectId: string; readonly endpoint?: ProjectEndpoint }) {
  if (!endpoint?.caCertPem) {
    return (
      <p className="text-xs text-text-tertiary" data-testid="conn-ca-pending">
        Full certificate verification (sslmode=verify-full) needs the platform certificate authority. It is published
        with the project&apos;s external endpoint.
      </p>
    );
  }
  const href = `data:application/x-pem-file;base64,${btoa(endpoint.caCertPem)}`;
  return (
    <a
      href={href}
      download={`${projectId}-ca.crt`}
      data-testid="conn-ca-download"
      className="inline-flex items-center gap-2 px-3 py-2 rounded border border-border-primary text-sm text-text-secondary hover:text-text-primary hover:border-text-secondary transition-colors"
    >
      <Download className="w-4 h-4" /> Download certificate authority
    </a>
  );
}

// ConnectionStrings shows what a customer has to paste into a client, for
// Postgres and — on a DocumentDB project — for Mongo.
export function ConnectionStrings({ projectId, documentDb, endpoint }: ConnectionStringsProps) {
  const { data: credentials, isLoading, error } = useCredentials(projectId);
  const [revealed, setRevealed] = useState(false);

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <Loader2 className="w-6 h-6 animate-spin text-accent-primary" />
      </div>
    );
  }
  if (error || !credentials) {
    return (
      <div className="rounded-lg border border-color-error bg-red-900/20 p-4">
        <p className="text-color-error text-sm">Connection details could not be loaded.</p>
      </div>
    );
  }

  const target = resolveTarget(credentials, endpoint);
  const shownPassword = revealed ? credentials.password : MASKED;
  const shownUri = postgresUri(credentials, target, shownPassword);
  const realUri = postgresUri(credentials, target, credentials.password);

  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4 space-y-4" data-testid="connection-strings">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h4 className="text-sm font-medium text-text-primary">Connect</h4>
          <p className="text-xs text-text-tertiary mt-1" data-testid="conn-scope">
            {target.scope === 'public'
              ? 'Public endpoint — reachable from outside the cluster.'
              : 'In-cluster endpoint — reachable from workloads in the cluster. No public endpoint is published for this project yet.'}
          </p>
        </div>
        <button
          type="button"
          onClick={() => setRevealed(!revealed)}
          data-testid="conn-reveal-password"
          aria-label={revealed ? 'Hide password' : 'Show password'}
          className="p-2 rounded border border-border-primary text-text-secondary hover:text-text-primary hover:border-text-secondary transition-colors"
        >
          {revealed ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
        </button>
      </div>

      <StringRow testId="conn-postgres-uri" label="PostgreSQL connection URI" shown={shownUri} copied={realUri} />
      <StringRow testId="conn-psql" label="psql" shown={`psql "${shownUri}"`} copied={`psql "${realUri}"`} />

      {documentDb && (
        <div className="border-t border-border-primary pt-4 space-y-2" data-testid="conn-mongo-section">
          <h5 className="text-sm font-medium text-text-primary">Mongo clients (DocumentDB)</h5>
          {target.mongoPort ? (
            <StringRow
              testId="conn-mongo-uri"
              label="MongoDB connection URI"
              shown={mongoUri(credentials, target, shownPassword, target.mongoPort)}
              copied={mongoUri(credentials, target, credentials.password, target.mongoPort)}
            />
          ) : (
            <p className="text-xs text-text-tertiary" data-testid="conn-mongo-pending">
              This project carries DocumentDB, but no Mongo port is published for it yet. The Mongo connection string
              appears here once the project&apos;s external endpoint exposes one.
            </p>
          )}
        </div>
      )}

      <div className="border-t border-border-primary pt-4">
        <CertificateAuthority projectId={projectId} endpoint={endpoint} />
      </div>
    </div>
  );
}
