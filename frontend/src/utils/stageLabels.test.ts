import { describe, test, expect } from 'vitest';
import { STAGE_LABELS, failureHeadline, stageLabel } from './stageLabels';

describe('stageLabel', () => {
  test('maps a stage to its product word', () => {
    expect(stageLabel('NAMESPACE_CREATION')).toBe('Environment');
    expect(stageLabel('CRD_DEPLOYMENT')).toBe('Database');
  });

  test('has no label for an unknown or missing stage', () => {
    expect(stageLabel('SOMETHING_NEW')).toBeUndefined();
    expect(stageLabel(undefined)).toBeUndefined();
  });

  test('does not read inherited object keys as stages', () => {
    expect(stageLabel('constructor')).toBeUndefined();
    expect(stageLabel('toString')).toBeUndefined();
  });

  test('every label is a plain word without the raw key', () => {
    for (const [key, label] of Object.entries(STAGE_LABELS)) expect(label).not.toBe(key);
  });
});

describe('failureHeadline', () => {
  test('names the stage when it is known', () => {
    expect(failureHeadline('CRD_DEPLOYMENT')).toBe('Provisioning failed at stage Database');
  });

  test.each([undefined, '', 'FAILED', 'SOMETHING_NEW'])('says only "Provisioning failed" for the stage %s', (stage) => {
    expect(failureHeadline(stage)).toBe('Provisioning failed');
  });
});
