import { describe, test, expect } from 'vitest';
import { stageLabel } from './stageLabels';

describe('stageLabel', () => {
  test('maps a stage to its product word', () => {
    expect(stageLabel('NAMESPACE_CREATION')).toBe('Environment');
    expect(stageLabel('CRD_DEPLOYMENT')).toBe('Database');
  });

  test('falls back for an unknown or missing stage', () => {
    expect(stageLabel('SOMETHING_NEW')).toBe('an unknown step');
    expect(stageLabel(undefined)).toBe('an unknown step');
  });
});
