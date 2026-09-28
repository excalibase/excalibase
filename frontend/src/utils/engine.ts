import type { DatabaseInstance } from '../types';

type EngineFields = Pick<DatabaseInstance, 'documentDb'> & { databaseType: string };

export const DOCUMENTDB_LABEL = 'DocumentDB (MongoDB-compatible)';

// A DocumentDB project is a Postgres project created with documentDb set, so
// the flag, not databaseType, is what tells the two apart.
export function engineLabel(project: EngineFields): string {
  if (project.documentDb === true) return DOCUMENTDB_LABEL;
  if (project.databaseType === 'POSTGRESQL') return 'PostgreSQL';
  return project.databaseType;
}

export function engineIcon(project: EngineFields): string {
  if (project.documentDb === true) return '🍃';
  if (project.databaseType === 'POSTGRESQL') return '🐘';
  if (project.databaseType === 'MYSQL') return '🐬';
  return '🗄️';
}
