import { describe, test, expect } from 'vitest';
import { engineIcon, engineLabel } from './engine';

describe('engineLabel', () => {
  test('names a DocumentDB project by what its clients speak', () => {
    expect(engineLabel({ databaseType: 'POSTGRESQL', documentDb: true })).toBe('DocumentDB (MongoDB-compatible)');
  });

  test('names a plain Postgres project PostgreSQL', () => {
    expect(engineLabel({ databaseType: 'POSTGRESQL', documentDb: false })).toBe('PostgreSQL');
    expect(engineLabel({ databaseType: 'POSTGRESQL' })).toBe('PostgreSQL');
  });

  test('passes an engine it does not know through unchanged', () => {
    expect(engineLabel({ databaseType: 'MYSQL' })).toBe('MYSQL');
  });
});

describe('engineIcon', () => {
  test('gives DocumentDB its own mark', () => {
    expect(engineIcon({ databaseType: 'POSTGRESQL', documentDb: true })).not.toBe(
      engineIcon({ databaseType: 'POSTGRESQL' })
    );
  });
});
