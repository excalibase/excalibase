import { describe, test, expect } from 'vitest';
import { engineIcon, engineLabel } from './engine';

describe('engine of a project without a database', () => {
  test('says there is none', () => {
    expect(engineLabel({ databaseType: '', noDatabase: true })).toBe('No database');
    expect(engineIcon({ databaseType: '', noDatabase: true })).toBe('📦');
  });
});
