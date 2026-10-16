import { describe, test, expect } from 'vitest';
import { MySQL, PostgreSQL } from '@codemirror/lang-sql';
import {
  columnTypesFor, defaultSuggestionsFor, idColumnTypeFor, indexTypesFor, sqlDialectFor,
} from './engineSql';

// ADR 0041 decision 9: one editor, one table editor, each reading its engine.
describe('sqlDialectFor', () => {
  test('a MySQL project highlights and completes MySQL', () => {
    expect(sqlDialectFor({ databaseType: 'MYSQL' })).toBe(MySQL);
  });

  test('every other project, and one still loading, gets PostgreSQL', () => {
    expect(sqlDialectFor({ databaseType: 'POSTGRESQL' })).toBe(PostgreSQL);
    expect(sqlDialectFor(undefined)).toBe(PostgreSQL);
  });
});

describe('column types', () => {
  test('the Postgres list is the one Studio always offered', () => {
    expect(columnTypesFor(false)).toEqual([
      'text', 'integer', 'bigint', 'serial', 'boolean', 'timestamp', 'timestamptz', 'date', 'numeric', 'uuid', 'jsonb', 'varchar(255)',
    ]);
    expect(idColumnTypeFor(false)).toBe('serial');
  });

  test('the MySQL list speaks MySQL and none of the Postgres-only types', () => {
    const types = columnTypesFor(true);
    for (const expected of [
      'int', 'bigint', 'bigint unsigned auto_increment', 'tinyint(1)', 'smallint', 'decimal(10,2)', 'double',
      'varchar(255)', 'char(36)', 'text', 'mediumtext', 'json', 'date', 'datetime', 'timestamp', 'time', "enum('a','b')", 'blob',
    ]) {
      expect(types).toContain(expected);
    }
    for (const postgresOnly of ['serial', 'timestamptz', 'jsonb', 'uuid', 'boolean']) {
      expect(types).not.toContain(postgresOnly);
    }
    expect(idColumnTypeFor(true)).toBe('bigint unsigned auto_increment');
  });

  test('default suggestions follow the engine', () => {
    expect(defaultSuggestionsFor(true)).toEqual(['CURRENT_TIMESTAMP', '(UUID())', 'NULL']);
    expect(defaultSuggestionsFor(false)).toEqual(['now()', 'gen_random_uuid()', 'NULL']);
  });

  test('MySQL indexes are B-trees; Postgres keeps its access methods', () => {
    expect(indexTypesFor(true)).toEqual(['btree']);
    expect(indexTypesFor(false)).toEqual(['btree', 'hash', 'gin', 'gist', 'brin']);
  });
});
