import { describe, expect, test } from 'vitest';
import { siteNameProblem } from './siteName';

// Mirrors server-go/internal/sites/site.go ValidateName.
describe('siteNameProblem', () => {
  test.each(['my-app', 'abc', 'a1b', 'shop-2026', 'a'.repeat(63)])('accepts %s', (name) => {
    expect(siteNameProblem(name)).toBeNull();
  });

  test.each([
    ['ab', 'too short'],
    ['a'.repeat(64), 'too long'],
    ['My-App', 'uppercase'],
    ['-app', 'leading hyphen'],
    ['app-', 'trailing hyphen'],
    ['my_app', 'underscore'],
    ['my.app', 'dot'],
  ])('refuses %s (%s) with the shape rule', (name) => {
    expect(siteNameProblem(name)).toMatch(/3 to 63 lowercase letters, digits or hyphens/);
  });

  test('refuses two hyphens in a row', () => {
    expect(siteNameProblem('my--app')).toMatch(/two hyphens in a row/);
  });

  test.each(['www', 'api', 'studio', 'app', 'apps', 'graphql', 'staging'])(
    'refuses the reserved name %s',
    (name) => {
      expect(siteNameProblem(name)).toMatch(/reserved/);
    },
  );

  test('refuses any name holding excalibase', () => {
    expect(siteNameProblem('my-excalibase-shop')).toMatch(/reserved/);
  });
});
