import { Loader2 } from 'lucide-react';
import { useCredentials } from '../hooks/useProvisioning';
import type { ProjectEndpoint } from '../api/projectEndpoint';
import type { CredentialsResponse } from '../types';
import { SshTunnelNote } from './SshTunnelNote';
import { CertificateAuthority } from './CertificateAuthority';
import { NOT_REACHABLE, StringRow, internalTarget, labels, publicTarget, userinfo, type SectionProps, type Target } from './ConnectionParts';
import { ResetPasswordButton } from './ResetPasswordButton';
import { useHostsApps } from '../hooks/useHostsApps';
import { MySQLSection } from './MySQLConnection';

interface ConnectionStringsProps {
  readonly projectId: string;
  // Whether this project's image carries DocumentDB, so Mongo clients have
  // something to talk to. A project without it gets no Mongo section at all.
  readonly documentDb: boolean;
  // The project's database endpoint, as the control plane reports it.
  readonly endpoint?: ProjectEndpoint;
}

// A DocumentDB project is one database, one credential, two protocols. The
// username and password work with psql and with a Mongo driver alike; the
// password is never stored, so every string carries a placeholder for it.

function postgresUri(credentials: CredentialsResponse, target: Target, sslMode: string): string {
  return `postgresql://${userinfo(credentials.username)}@${target.host}:${target.port}/${credentials.databaseName}?sslmode=${sslMode}`;
}

// The shape the control plane renders and the gateway was proven to accept.
// TLS is spelled the way a driver reads it — `tls`, never libpq's `sslmode`.
// directConnection: the gateway is one server, and topology discovery would
// follow a member address only the cluster can reach.
function mongoUri(credentials: CredentialsResponse, target: Target, tls: boolean): string {
  return `mongodb://${userinfo(credentials.username)}@${target.host}:${target.port}/?tls=${tls}&authMechanism=SCRAM-SHA-256&directConnection=true`;
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

// A single host's Postgres serves no TLS; its strings say so rather than prefer it.
function postgresModes(credentials: CredentialsResponse, endpoint?: ProjectEndpoint): { internal: string; external: string } {
  if (endpoint?.singleHost) return { internal: 'disable', external: 'disable' };
  return {
    internal: endpoint ? 'prefer' : (credentials.sslMode ?? 'require'),
    external: endpoint?.requireTls ? 'verify-full' : 'prefer',
  };
}

function mongoInternalTarget(endpoint?: ProjectEndpoint): Target | null {
  const internal = endpoint?.mongo?.internal;
  if (!internal) return null;
  return { host: internal.host, port: internal.port };
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

function PostgresSection({ projectId, credentials, endpoint, hostsApps }: SectionProps) {
  const internal = internalTarget(credentials, endpoint);
  const external = publicTarget(endpoint);
  const modes = postgresModes(credentials, endpoint);
  const label = labels(endpoint);

  return (
    <div className="space-y-2" data-testid="conn-postgres-section">
      <h5 className="text-sm font-medium text-text-primary">PostgreSQL</h5>
      {hostsApps && (
        <StringRow testId="conn-postgres-internal" label={label.internal} value={postgresUri(credentials, internal, modes.internal)} />
      )}
      {external ? (
        <StringRow testId="conn-postgres-public" label={label.external} value={postgresUri(credentials, external, modes.external)} />
      ) : (
        <p className="text-xs text-text-tertiary" data-testid="conn-postgres-public-absent">{NOT_REACHABLE}</p>
      )}
      {external && endpoint?.singleHost && <SshTunnelNote port={external.port} />}
      {!endpoint?.singleHost && (external || hostsApps) && <CertificateAuthority projectId={projectId} pem={endpoint?.caCertificate ?? ''} />}
    </div>
  );
}

function MongoSection({ projectId, endpoint, credentials, hostsApps }: SectionProps) {
  const internal = hostsApps ? mongoInternalTarget(endpoint) : null;
  const external = mongoPublicTarget(endpoint);
  const label = labels(endpoint);

  return (
    <div className="border-t border-border-primary pt-4 space-y-2" data-testid="conn-mongo-section">
      <h5 className="text-sm font-medium text-text-primary">MongoDB (DocumentDB)</h5>
      {internal && (
        <StringRow testId="conn-mongo-internal" label={label.internal} value={mongoUri(credentials, internal, true)} />
      )}
      {external && (
        // The gateway takes TLS only; Require TLS changes Postgres alone (EXC-530).
        <StringRow testId="conn-mongo-public" label={label.external} value={mongoUri(credentials, external, true)} />
      )}
      {external && endpoint?.singleHost && <SshTunnelNote port={external.port} />}
      {(internal || external) && <CertificateAuthority projectId={projectId} pem={endpoint?.caCertificate ?? ''} />}
      {!external && (!internal || endpoint?.mongo?.port) && (
        <p className="text-xs text-text-tertiary" data-testid="conn-mongo-unavailable">
          {endpoint?.publicEnabled
            ? 'This project carries DocumentDB, but its MongoDB endpoint is not answering yet — it starts after the database and creates its user first. The PostgreSQL endpoint above already works, and both speak to the same database with the same credential.'
            : NOT_REACHABLE}
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
  const hostsApps = useHostsApps(projectId);

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

  const sectionProps = { projectId, credentials, endpoint, hostsApps };

  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4 space-y-4" data-testid="connection-strings">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h4 className="text-sm font-medium text-text-primary">Connect</h4>
          <p className="text-xs text-text-tertiary mt-1">
            One database, one credential. {documentDb ? 'The same username and password works with psql and with a Mongo driver.' : ''}
          </p>
        </div>
        <ResetPasswordButton projectId={projectId} />
      </div>

      <div className="grid grid-cols-2 gap-3">
        <CredentialField testId="conn-username" label="Username" value={credentials.username} />
        <div>
          <div className="text-xs text-text-tertiary mb-1">Password</div>
          <p className="text-xs text-text-secondary" data-testid="conn-password-note">
            The password is shown once, when you create the database or reset it, and is not stored. Reset password to get new credentials.
          </p>
        </div>
      </div>

      {credentials.readerUsername && (
        <div>
          <p className="text-xs text-text-tertiary mb-2">
            Read-only login: it can read every table and change nothing. The read address signs in with it.
          </p>
          <CredentialField testId="conn-reader-username" label="Read-only username" value={credentials.readerUsername} />
        </div>
      )}

      {credentials.engine === 'mysql' ? (
        <MySQLSection {...sectionProps} />
      ) : (
        <>
          <PostgresSection {...sectionProps} />
          {documentDb && <MongoSection {...sectionProps} />}
        </>
      )}
    </div>
  );
}
