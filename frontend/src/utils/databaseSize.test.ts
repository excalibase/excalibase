import { describe, test, expect } from 'vitest';
import { databaseSize } from './databaseSize';

// EXC-531: the exact size the server reports (documents included for a
// DocumentDB project), not a whole-GB figure that reads 0 for most projects.
describe('databaseSize', () => {
  test('shows the exact bytes when the server reports them', () => {
    expect(databaseSize({ databaseSizeBytes: 58720256 })).toBe('56.0 MB');
  });
  test('says nothing is known when no size is reported', () => {
    expect(databaseSize({ databaseSizeBytes: null })).toBeUndefined();
  });
});
