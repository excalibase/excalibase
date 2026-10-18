import { useState } from 'react';
import { Eye, EyeOff, Loader2 } from 'lucide-react';
import { useCredentials } from '../hooks/useProvisioning';
import type { ProjectEndpoint } from '../api/projectEndpoint';
import type { CredentialsResponse } from '../types';
import { SshTunnelNote } from './SshTunnelNote';
import { CertificateAuthority } from './CertificateAuthority';
import { MASKED, StringRow, internalTarget, labels, publicTarget, userinfo, type SectionProps, type Target } from './ConnectionParts';
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
// username and password below work with psql and with a Mongo driver alike,
// so they are shown once and every string on this card is built from them.

function postgresUri(credentials: CredentialsResponse, target: Target, password: string, sslMode: string): string {
  return `postgresql://${userinfo(credentials.username, password)}@${target.host}:${target.port}/${credentials.databaseName}?sslmode=${sslMode}`;
}

// The shape the control plane renders and the gateway was proven to accept.
// TLS is spelled the way a driver reads it — `tls`, never libpq's `sslmode`.
// directConnection: the gateway is one server, and topology discovery would
// follow a member address only the cluster can reach.
function mongoUri(credentials: CredentialsResponse, target: Target, password: string, tls: boolean): string {
  return `mongodb://${userinfo(credentials.username, password)}@${target.host}:${target.port}/?tls=${tls}&authMechanism=SCRAM-SHA-256&directConnection=true`;
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

function PostgresSection({ projectId, credentials, endpoint, shownPassword }: SectionProps) {
  const internal = internalTarget(credentials, endpoint);
  const external = publicTarget(endpoint);
  const modes = postgresModes(credentials, endpoint);
  const label = labels(endpoint);
  const row = (target: Target, mode: string) => ({
    shown: postgresUri(credentials, target, shownPassword, mode),
    copied: postgresUri(credentials, target, credentials.password, mode),
  });

  return (
    <div className="space-y-2" data-testid="conn-postgres-section">
      <h5 className="text-sm font-medium text-text-primary">PostgreSQL</h5>
      <StringRow testId="conn-postgres-internal" label={label.internal} {...row(internal, modes.internal)} />
      {external ? (
        <StringRow testId="conn-postgres-public" label={label.external} {...row(external, modes.external)} />
      ) : (
        <p className="text-xs text-text-tertiary" data-testid="conn-postgres-public-absent">
          This project publishes no public port. Use the internal address, or ask an admin to open one under Public
          database port.
        </p>
      )}
      {external && endpoint?.singleHost && <SshTunnelNote port={external.port} />}
      {!endpoint?.singleHost && <CertificateAuthority projectId={projectId} pem={endpoint?.caCertificate ?? ''} />}
    </div>
  );
}

function MongoSection({ projectId, credentials, endpoint, shownPassword }: SectionProps) {
  const internal = mongoInternalTarget(endpoint);
  const external = mongoPublicTarget(endpoint);
  const label = labels(endpoint);
  const row = (target: Target, tls: boolean) => ({
    shown: mongoUri(credentials, target, shownPassword, tls),
    copied: mongoUri(credentials, target, credentials.password, tls),
  });

  return (
    <div className="border-t border-border-primary pt-4 space-y-2" data-testid="conn-mongo-section">
      <h5 className="text-sm font-medium text-text-primary">MongoDB (DocumentDB)</h5>
      {internal && (
        <StringRow testId="conn-mongo-internal" label={label.internal} {...row(internal, true)} />
      )}
      {external && (
        <StringRow
          testId="conn-mongo-public"
          label={label.external}
          // The gateway takes TLS only; Require TLS changes Postgres alone (EXC-530).
          {...row(external, true)}
        />
      )}
      {external && endpoint?.singleHost && <SshTunnelNote port={external.port} />}
      {(internal || external) && <CertificateAuthority projectId={projectId} pem={endpoint?.caCertificate ?? ''} />}
      {!external && (!internal || endpoint?.mongo?.port) && (
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
