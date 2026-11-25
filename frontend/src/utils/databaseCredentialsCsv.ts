import { csvCell } from './csvCell';

export interface CredentialsRow {
  readonly host: string;
  readonly port: string;
  readonly database: string;
  readonly user: string;
  readonly password: string;
  readonly sslmode: string;
}

const HEADER = 'host,port,database,user,password,sslmode';

export function credentialsCsv(row: CredentialsRow): string {
  const cells = [row.host, row.port, row.database, row.user].map(csvCell);
  const tail = [row.sslmode].map(csvCell);
  // Quoted as typed: defusing a leading formula character would change the password.
  const password = `"${row.password.replaceAll('"', '""')}"`;
  return `${HEADER}\n${[...cells, password, ...tail].join(',')}\n`;
}

export function credentialsCsvFileName(projectId: string): string {
  const safe = projectId.replaceAll(/[^A-Za-z0-9_-]+/g, '-').replaceAll(/^-+|-+$/g, '');
  return `${safe || 'project'}-database-credentials.csv`;
}
