import { useState } from 'react';
import { Copy, Check, Eye, EyeOff, Loader2, Download } from 'lucide-react';
import { useCredentials } from '../hooks/useProvisioning';
import type { ProjectEndpoint } from '../api/projectEndpoint';
import type { CredentialsResponse } from '../types';

interface ConnectionStringsProps {
  readonly projectId: string;
  // Whether this project's image carries DocumentDB, so Mongo clients have
  // something to talk to. A project without it gets no Mongo section at all.
  readonly documentDb: boolean;
  // The project's database endpoint, as the control plane reports it.
  readonly endpoint?: ProjectEndpoint;
}

// A DocumentDB project is one database, one credential, two protocols. The
// username and password below work with psql and with a Mongo driver alike,
// so they are shown once and every string on this card is built from them.

const MASKED = '••••••••';

// A target is one address a client can dial. Every project has an internal
// one; a public one exists only when the customer opened a port and it is
// answering.
interface Target {
  host: string;
  port: number;
}

function postgresUri(credentials: CredentialsResponse, target: Target, password: string, sslMode: string): string {
  return `postgresql://${credentials.username}:${password}@${target.host}:${target.port}/${credentials.databaseName}?sslmode=${sslMode}`;
}

// A Mongo client authenticates against the database it connects to, so the
// database name carries both the path and authSource. TLS is spelled the way
// a driver reads it — `tls`, never libpq's `sslmode`, which a driver rejects.
function mongoUri(credentials: CredentialsResponse, target: Target, password: string, tls: boolean): string {
  return `mongodb://${credentials.username}:${password}@${target.host}:${target.port}/${credentials.databaseName}?authSource=${credentials.databaseName}&tls=${tls}`;
}

// internalTarget is where a workload inside the cluster connects. It is shown
// whether or not the project publishes publicly: an app hosted beside the
// database should use it and needs no public port.
function internalTarget(credentials: CredentialsResponse, endpoint?: ProjectEndpoint): Target {
  if (endpoint) return { host: endpoint.internal.host, port: endpoint.internal.port };
  return { host: credentials.host, port: credentials.port };
}

// publicTarget is the port a client outside the cluster dials, or null when
// the project publishes none or it is not answering yet.
function publicTarget(endpoint?: ProjectEndpoint): Target | null {
  if (!endpoint?.publicEnabled || !endpoint.available || endpoint.port <= 0) return null;
  return { host: endpoint.host, port: endpoint.port };
}

// mongoPublicTarget is the public Mongo port. The gateway waits for the
// database and creates its user before it answers, so this stays null — and
// the page says so — while Postgres is already usable.
function mongoPublicTarget(endpoint?: ProjectEndpoint): Target | null {
  const external = publicTarget(endpoint);
  const mongo = endpoint?.mongo;
  if (!external || !mongo?.available || !mongo.port) return null;
  return { host: external.host, port: mongo.port };
}

function mongoInternalTarget(endpoint?: ProjectEndpoint): Target | null {
  const mongo = endpoint?.mongo;
  if (!mongo?.available || !mongo.internal) return null;
  return { host: mongo.internal.host, port: mongo.internal.port };
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
      <div className="text-xs text-text-tertiary mb-1" data-testid={`${testId}-label`}>{label}</div>
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

function CredentialField({ testId, label, value }: { readonly testId: string; readonly label: string; readonly value: string }) {
  return (
    <div>
      <div className="text-xs text-text-tertiary mb-1">{label}</div>
      <code
        data-testid={testId}
        className="block bg-bg-tertiary border border-border-primary rounded px-3 py-2 text-xs font-mono text-text-primary break-all"
      >
        {value}
      </code>
    </div>
  );
}

// Full verification needs the certificate authority as a file on the client,
// not a value inside the URI — which is why it sits beside the Mongo string
// as well as the Postgres one.
function CertificateAuthority({ projectId, pem }: { readonly projectId: string; readonly pem: string }) {
  if (!pem) return null;
  const href = `data:application/x-pem-file;base64,${btoa(pem)}`;
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

interface SectionProps {
  readonly projectId: string;
  readonly credentials: CredentialsResponse;
  readonly endpoint?: ProjectEndpoint;
  readonly shownPassword: string;
}

function PostgresSection({ projectId, credentials, endpoint, shownPassword }: SectionProps) {
  const internal = internalTarget(credentials, endpoint);
  const external = publicTarget(endpoint);
  const internalMode = endpoint ? 'prefer' : (credentials.sslMode ?? 'require');
  const publicMode = endpoint?.requireTls ? 'verify-full' : 'prefer';
  const row = (target: Target, mode: string) => ({
    shown: postgresUri(credentials, target, shownPassword, mode),
    copied: postgresUri(credentials, target, credentials.password, mode),
  });

  return (
    <div className="space-y-2" data-testid="conn-postgres-section">
      <h5 className="text-sm font-medium text-text-primary">PostgreSQL</h5>
      <StringRow testId="conn-postgres-internal" label="Internal — from inside the cluster" {...row(internal, internalMode)} />
      {external ? (
        <>
          <StringRow testId="conn-postgres-public" label="Public — from outside the cluster" {...row(external, publicMode)} />
          <CertificateAuthority projectId={projectId} pem={endpoint?.caCertificate ?? ''} />
        </>
      ) : (
        <p className="text-xs text-text-tertiary" data-testid="conn-postgres-public-absent">
          This project publishes no public port. Use the internal address, or open a port in the database endpoint
          settings.
        </p>
      )}
    </div>
  );
}

function MongoSection({ projectId, credentials, endpoint, shownPassword }: SectionProps) {
  const internal = mongoInternalTarget(endpoint);
  const external = mongoPublicTarget(endpoint);
  const row = (target: Target, tls: boolean) => ({
    shown: mongoUri(credentials, target, shownPassword, tls),
    copied: mongoUri(credentials, target, credentials.password, tls),
  });

  return (
    <div className="border-t border-border-primary pt-4 space-y-2" data-testid="conn-mongo-section">
      <h5 className="text-sm font-medium text-text-primary">MongoDB (DocumentDB)</h5>
      {internal && (
        <StringRow testId="conn-mongo-internal" label="Internal — from inside the cluster" {...row(internal, false)} />
      )}
      {external && (
        <>
          <StringRow
            testId="conn-mongo-public"
            label="Public — from outside the cluster"
            {...row(external, endpoint?.requireTls ?? true)}
          />
          <CertificateAuthority projectId={projectId} pem={endpoint?.caCertificate ?? ''} />
        </>
      )}
      {!internal && !external && (
        <p className="text-xs text-text-tertiary" data-testid="conn-mongo-unavailable">
          This project carries DocumentDB, but its MongoDB endpoint is not answering yet — it starts after the database
          and creates its user first. The PostgreSQL endpoint above already works, and both speak to the same database
          with the same credential.
        </p>
      )}
    </div>
  );
}

// ConnectionStrings shows the project's one credential and, beside it, the
// strings a client pastes: PostgreSQL always, MongoDB only on a project whose
// image carries DocumentDB.
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

  const shownPassword = revealed ? credentials.password : MASKED;
  const sectionProps = { projectId, credentials, endpoint, shownPassword };

  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4 space-y-4" data-testid="connection-strings">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h4 className="text-sm font-medium text-text-primary">Connect</h4>
          <p className="text-xs text-text-tertiary mt-1">
            One database, one credential. {documentDb ? 'The same username and password works with psql and with a Mongo driver.' : ''}
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

      <div className="grid grid-cols-2 gap-3">
        <CredentialField testId="conn-username" label="Username" value={credentials.username} />
        <CredentialField testId="conn-password" label="Password" value={shownPassword} />
      </div>

      <PostgresSection {...sectionProps} />
      {documentDb && <MongoSection {...sectionProps} />}
    </div>
  );
}
