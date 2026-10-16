import { SshTunnelNote } from './SshTunnelNote';
import { StringRow, internalTarget, labels, publicTarget, type SectionProps, type Target } from './ConnectionParts';

// The Node example takes the password from the environment, so no secret is
// ever part of a snippet someone pastes into code.
function nodeExample(target: Target, username: string, database: string): string {
  return `import mysql from 'mysql2/promise';

const connection = await mysql.createConnection({
  host: '${target.host}',
  port: ${target.port},
  user: '${username}',
  password: process.env.MYSQL_PASSWORD,
  database: '${database}',
  ssl: { rejectUnauthorized: false },
});`;
}

function connectorJExample(jdbcUrl: string, username: string): string {
  return `String url = "${jdbcUrl}";
try (Connection connection = DriverManager.getConnection(url, "${username}", System.getenv("MYSQL_PASSWORD"))) {
    // ...
}`;
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
// TLS, so each string asks for it.
export function MySQLSection({ credentials, endpoint, shownPassword }: SectionProps) {
  const uri = (target: Target, password: string) =>
    `mysql://${credentials.username}:${password}@${target.host}:${target.port}/${credentials.databaseName}?ssl-mode=REQUIRED`;
  const row = (target: Target) => ({ shown: uri(target, shownPassword), copied: uri(target, credentials.password) });
  const internal = internalTarget(credentials, endpoint);
  const external = publicTarget(endpoint);
  const label = labels(endpoint);
  const jdbcUrl = credentials.jdbcUrl
    ?? `jdbc:mysql://${internal.host}:${internal.port}/${credentials.databaseName}?sslMode=REQUIRED`;
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
      {credentials.jdbcUrl && (
        <StringRow testId="conn-mysql-jdbc" label="JDBC (Connector/J) — pass the username and password separately" shown={credentials.jdbcUrl} copied={credentials.jdbcUrl} />
      )}
      <Snippet testId="conn-mysql-node" label="Node (mysql2)" code={nodeExample(internal, credentials.username, credentials.databaseName)} />
      <Snippet testId="conn-mysql-connectorj" label="Java (Connector/J)" code={connectorJExample(jdbcUrl, credentials.username)} />
      <p className="text-xs text-text-tertiary" data-testid="conn-mysql-tls-note">
        TLS is on for every connection; the server certificate is not verified yet, as with ssl-mode=REQUIRED.
      </p>
    </div>
  );
}
