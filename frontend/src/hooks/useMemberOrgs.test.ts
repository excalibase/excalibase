import { describe, test, expect } from 'vitest';
import { memberOrgsOf } from './useMemberOrgs';

const org = (id: string, extra: object = {}) => ({ id, name: id, slug: id, tier: 'FREE', ownerId: 'someone', ...extra });

describe('memberOrgsOf', () => {
  test('keeps orgs the listing marks with the caller role', () => {
    expect(memberOrgsOf([org('a', { role: 'developer' }), org('b')], 'u1').map((o) => o.id)).toEqual(['a']);
  });

  test('an admin listing of every org keeps only the ones the admin owns', () => {
    const all = [org('a', { ownerId: 'u1' }), org('b'), org('c')];
    expect(memberOrgsOf(all, 'u1').map((o) => o.id)).toEqual(['a']);
  });

  test('no user means no orgs', () => {
    expect(memberOrgsOf([org('a', { role: 'owner' })], undefined)).toEqual([]);
  });
});
