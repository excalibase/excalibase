import { SshTunnelNote } from './SshTunnelNote';
import { CertificateAuthority, caFileName } from './CertificateAuthority';
import { StringRow, internalTarget, labels, publicTarget, type SectionProps, type Target } from './ConnectionParts';

// The Node example takes the password from the environment, so no secret is
// ever part of a snippet someone pastes into code.
function nodeExample(target: Target, username: string, database: string, caFile: string): string {
  return `import { readFileSync } from 'node:fs';
import mysql from 'mysql2/promise';

const connection = await mysql.createConnection({
  host: '${target.host}',
  port: ${target.port},
  user: '${username}',
  password: process.env.MYSQL_PASSWORD,
  database: '${database}',
  ssl: { ca: readFileSync('${caFile}') },
});`;
}

function connectorJExample(jdbcUrl: string, username: string): string {
  return `String url = "${jdbcUrl}";
try (Connection connection = DriverManager.getConnection(url, "${username}", System.getenv("MYSQL_PASSWORD"))) {
    // ...
}`;
}

// Connector/J verifies against a trust store, not a PEM file.
function TrustStoreHint({ projectId }: { readonly projectId: string }) {
  const store = `${projectId}-ca.p12`;
  return (
    <p className="text-xs text-text-tertiary" data-testid="conn-mysql-connectorj-truststore">
      Connector/J reads a trust store: <code>keytool -importcert -alias excalibase-ca -file {caFileName(projectId)} -keystore {store} -storetype PKCS12</code>,
      then add <code>&amp;trustCertificateKeyStoreUrl=file:{store}&amp;trustCertificateKeyStoreType=PKCS12&amp;trustCertificateKeyStorePassword=…</code> to the URL.
    </p>
  );
}

function Snippet({ testId, label, code }: { readonly testId: string; readonly label: string; readonly code: string }) {
  return (
    <div>
      <div className="text-xs text-text-tertiary mb-1">{label}</div>
      <pre
        data-testid={testId}
        className="bg-bg-tertiary border border-border-primary rounded px-3 py-2 text-xs font-mono text-text-primary overflow-x-auto"
      >
        {code}
      </pre>
    </div>
  );
}

// MySQLSection shows the strings a MySQL client takes. Every login requires
// TLS and the server certificate names every address shown here, so each
// string verifies it against the project's CA.
export function MySQLSection({ projectId, credentials, endpoint, shownPassword }: SectionProps) {
  const caFile = caFileName(projectId);
  const uri = (target: Target, password: string) =>
    `mysql://${credentials.username}:${password}@${target.host}:${target.port}/${credentials.databaseName}?ssl-mode=VERIFY_IDENTITY&ssl-ca=${caFile}`;
  const row = (target: Target) => ({ shown: uri(target, shownPassword), copied: uri(target, credentials.password) });
  const internal = internalTarget(credentials, endpoint);
  const external = publicTarget(endpoint);
  const label = labels(endpoint);
  const jdbcUrl = credentials.jdbcUrl
    ?? `jdbc:mysql://${internal.host}:${internal.port}/${credentials.databaseName}?sslMode=VERIFY_IDENTITY`;
  return (
    <div className="space-y-2" data-testid="conn-mysql-section">
      <h5 className="text-sm font-medium text-text-primary">MySQL — TLS required</h5>
      <StringRow testId="conn-mysql-internal" label={label.internal} {...row(internal)} />
      {external ? (
        <StringRow testId="conn-mysql-public" label={label.external} {...row(external)} />
      ) : (
        <p className="text-xs text-text-tertiary" data-testid="conn-mysql-public-absent">
          This project publishes no public port. Use the internal address, or ask an admin to open one under Public
          database port.
        </p>
      )}
      {external && endpoint?.singleHost && <SshTunnelNote port={external.port} />}
      <CertificateAuthority projectId={projectId} pem={endpoint?.caCertificate ?? ''} />
      {credentials.jdbcUrl && (
        <StringRow testId="conn-mysql-jdbc" label="JDBC (Connector/J) — pass the username and password separately" shown={credentials.jdbcUrl} copied={credentials.jdbcUrl} />
      )}
      <Snippet testId="conn-mysql-node" label="Node (mysql2)" code={nodeExample(internal, credentials.username, credentials.databaseName, caFile)} />
      <Snippet testId="conn-mysql-connectorj" label="Java (Connector/J)" code={connectorJExample(jdbcUrl, credentials.username)} />
      <TrustStoreHint projectId={projectId} />
      <p className="text-xs text-text-tertiary" data-testid="conn-mysql-tls-note">
        TLS is on for every connection, and the server certificate is verified against the project's certificate authority.
      </p>
    </div>
  );
}
