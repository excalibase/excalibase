import { describe, expect, test } from 'vitest';
import { requireSecondStep, SECOND_STEP_CHECK, validateBoolExp } from './permissionRules';

// EXC-680: a permission may compare a session variable with a fixed value, as the server and engine read it.
describe('session variable against a fixed value', () => {
  test.each([
    { 'X-Excalibase-Aal': { _eq: 'aal2' } },
    { 'x-excalibase-aal': { _neq: 'aal1' } },
    { 'X-Excalibase-Aal': { _in: ['aal2', 'aal3'] }, 'X-Excalibase-Team': { _nin: [] } },
    { 'X-Excalibase-Aal': { _is_null: false } },
    { _or: [{ 'X-Excalibase-Aal': { _eq: 'aal2' } }, { public: { _eq: true } }] },
  ])('accepted: %j', (exp) => {
    expect(validateBoolExp(exp)).toBeNull();
  });

  test.each([
    { 'X-Excalibase-Aal': { _gt: 'aal1' } },
    { 'X-Excalibase-Aal': { _like: 'aal%' } },
    { 'X-Excalibase-Aal': { _eq: 2 } },
    { 'X-Excalibase-Aal': { _eq: null } },
    { 'X-Excalibase-Aal': { _eq: 'X-Excalibase-User-Id' } },
    { 'X-Excalibase-Aal': { _in: ['aal2', 3] } },
    { 'X-Excalibase-Aal': { _in: 'X-Excalibase-Levels' } },
    { 'X-Excalibase-Aal': { _is_null: 'yes' } },
    { 'X-Excalibase-Aal': {} },
    { 'X-Excalibase-Aal': { level: { _eq: 'aal2' } } },
    { 'X-Excalibase-': { _eq: 'aal2' } },
  ])('refused: %j', (exp) => {
    expect(validateBoolExp(exp)).not.toBeNull();
  });
});

describe('requireSecondStep', () => {
  test('an empty or unreadable expression becomes the check alone', () => {
    expect(JSON.parse(requireSecondStep('{}'))).toEqual(SECOND_STEP_CHECK);
    expect(JSON.parse(requireSecondStep(''))).toEqual(SECOND_STEP_CHECK);
  });

  test('an existing expression is kept beside the check', () => {
    expect(JSON.parse(requireSecondStep('{"owner_id":{"_eq":"X-Excalibase-User-Id"}}'))).toEqual({
      _and: [{ owner_id: { _eq: 'X-Excalibase-User-Id' } }, SECOND_STEP_CHECK],
    });
  });

  test('text that is not JSON is left for the person to fix', () => {
    expect(requireSecondStep('{"owner_id":')).toBe('{"owner_id":');
  });

  test('asking twice adds it once', () => {
    const once = requireSecondStep('{"a":{"_eq":1}}');
    expect(requireSecondStep(once)).toBe(once);
  });
});
