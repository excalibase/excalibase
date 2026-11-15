import { describe, test, expect } from 'vitest';
import { PASSWORD_MAX, PASSWORD_MIN, passwordProblem } from './databasePassword';

describe('passwordProblem', () => {
  test('blank means generate for me', () => {
    expect(passwordProblem('')).toBeNull();
  });

  test('refuses a short password with the minimum', () => {
    expect(passwordProblem('short-pass-1')).toContain(String(PASSWORD_MIN));
  });

  test('refuses an over-long password', () => {
    expect(passwordProblem('ab1'.repeat(50))).toContain(String(PASSWORD_MAX));
  });

  test('refuses control characters', () => {
    expect(passwordProblem('abcdefgh1234567\u0007x')).toMatch(/control/i);
    expect(passwordProblem('abcdefgh1234567\nx')).toMatch(/control/i);
  });

  test('refuses fewer than eight different characters', () => {
    expect(passwordProblem('abababababababab')).toMatch(/different/i);
  });

  test('accepts a long random password and a passphrase of words', () => {
    expect(passwordProblem('k8Xq2Lm9Zp4Rt7Vw')).toBeNull();
    expect(passwordProblem('correct horse battery staple')).toBeNull();
  });

  test('refuses a password that starts with = + - or @', () => {
    for (const lead of ['=', '+', '-', '@']) {
      expect(passwordProblem(`${lead}starts-with-a-sign-x`)).toMatch(/cannot start with/);
    }
  });
});
