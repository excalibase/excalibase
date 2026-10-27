import { describe, test, expect } from 'vitest';
import { metadataMergePatch, parseMetadataObject } from './metadataPatch';

describe('metadataMergePatch', () => {
  test('sends changed and added keys with values and removed keys as null', () => {
    expect(
      metadataMergePatch({ plan: 'free', trial: true, seats: 1 }, { plan: 'pro', seats: 1, region: { id: 'eu' } }),
    ).toEqual({ plan: 'pro', trial: null, region: { id: 'eu' } });
  });

  test('is empty when nothing changed', () => {
    expect(metadataMergePatch({ a: { b: [1, 2] } }, { a: { b: [1, 2] } })).toEqual({});
  });

  test('keys named like object built-ins are ordinary keys', () => {
    expect(metadataMergePatch({}, { constructor: 1 })).toEqual({ constructor: 1 });
    expect(metadataMergePatch({ toString: 1 }, {})).toEqual({ toString: null });
  });

  test('a nested change replaces the whole top-level key', () => {
    expect(metadataMergePatch({ a: { b: 1, c: 2 } }, { a: { b: 1 } })).toEqual({ a: { b: 1 } });
  });
});

describe('parseMetadataObject', () => {
  test('accepts a JSON object', () => {
    expect(parseMetadataObject(' {"plan":"pro"} ')).toEqual({ value: { plan: 'pro' } });
  });

  test('an empty box is an empty object', () => {
    expect(parseMetadataObject('  ')).toEqual({ value: {} });
  });

  test.each(['[1]', 'null', '"plan"', '42', 'true'])('refuses %s, which is not an object', (text) => {
    expect(parseMetadataObject(text).error).toMatch(/must be a JSON object/);
  });

  test('refuses text that is not JSON', () => {
    expect(parseMetadataObject('{plan:pro}').error).toMatch(/not valid JSON/);
  });
});
