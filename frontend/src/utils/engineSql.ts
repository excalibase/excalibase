import { MySQL, PostgreSQL, type SQLDialect } from '@codemirror/lang-sql';
import { isMySQLProject } from './engine';

// The SQL a project's engine speaks, for the editor and the table editor
// (ADR 0041 decision 9): one page each, reading its engine from here.

const POSTGRES_COLUMN_TYPES = [
  'text', 'integer', 'bigint', 'serial', 'boolean', 'timestamp', 'timestamptz', 'date', 'numeric', 'uuid', 'jsonb', 'varchar(255)',
];

// tinyint(1) is MySQL's boolean; the API reads it as one.
const MYSQL_COLUMN_TYPES = [
  'int', 'bigint', 'bigint unsigned auto_increment', 'tinyint(1)', 'smallint', 'decimal(10,2)', 'double',
  'varchar(255)', 'char(36)', 'text', 'mediumtext', 'json', 'date', 'datetime', 'timestamp', 'time',
  "enum('a','b')", 'blob',
];

export function sqlDialectFor(project: { databaseType?: string } | undefined): SQLDialect {
  return isMySQLProject(project) ? MySQL : PostgreSQL;
}

export function columnTypesFor(mysql: boolean): string[] {
  return mysql ? MYSQL_COLUMN_TYPES : POSTGRES_COLUMN_TYPES;
}

export function idColumnTypeFor(mysql: boolean): string {
  return mysql ? 'bigint unsigned auto_increment' : 'serial';
}

export function defaultSuggestionsFor(mysql: boolean): string[] {
  return mysql ? ['CURRENT_TIMESTAMP', '(UUID())', 'NULL'] : ['now()', 'gen_random_uuid()', 'NULL'];
}

export function indexTypesFor(mysql: boolean): string[] {
  return mysql ? ['btree'] : ['btree', 'hash', 'gin', 'gist', 'brin'];
}
