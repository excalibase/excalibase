import { describe, test, expect } from 'vitest';
import { formatDiskBytes, sizeToBytes, splitDiskSize, toDiskSize } from './diskSize';

const MI = 1024 ** 2;
const GI = 1024 ** 3;

describe('formatDiskBytes', () => {
  test.each([
    [0, '0Mi'],
    [300 * 1024, '<1Mi'],
    [120 * MI, '120Mi'],
    [1023 * MI, '1023Mi'],
    [GI, '1Gi'],
    [1.5 * GI, '1.5Gi'],
    [10 * GI, '10Gi'],
    [2 * 1024 * GI, '2Ti'],
  ])('%d bytes reads %s', (bytes, expected) => {
    expect(formatDiskBytes(bytes)).toBe(expected);
  });
});

describe('sizeToBytes', () => {
  test('reads mebibytes and gibibytes', () => {
    expect(sizeToBytes('500Mi')).toBe(500 * MI);
    expect(sizeToBytes('10Gi')).toBe(10 * GI);
  });

  test('anything else is not a disk size', () => {
    expect(sizeToBytes('10GB')).toBeNull();
    expect(sizeToBytes('1.5Gi')).toBeNull();
    expect(sizeToBytes('')).toBeNull();
  });
});

describe('splitDiskSize', () => {
  test('splits a size into its amount and unit', () => {
    expect(splitDiskSize('500Mi')).toEqual({ amount: '500', unit: 'Mi' });
    expect(splitDiskSize('10Gi')).toEqual({ amount: '10', unit: 'Gi' });
  });
});

describe('toDiskSize', () => {
  test('a whole number of Mi from 64 up, or of Gi from 1 up, is a size', () => {
    expect(toDiskSize('500', 'Mi')).toEqual({ size: '500Mi', bytes: 500 * MI });
    expect(toDiskSize('64', 'Mi')).toEqual({ size: '64Mi', bytes: 64 * MI });
    expect(toDiskSize('3', 'Gi')).toEqual({ size: '3Gi', bytes: 3 * GI });
  });

  test.each([
    ['63', 'Mi'],
    ['0', 'Gi'],
    ['1.5', 'Gi'],
    ['', 'Gi'],
    ['-2', 'Gi'],
  ] as const)('%s%s is refused', (amount, unit) => {
    expect(toDiskSize(amount, unit)).toBeNull();
  });
});
