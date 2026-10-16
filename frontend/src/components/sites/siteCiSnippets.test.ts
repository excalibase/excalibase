import { describe, expect, test } from 'vitest';
import { siteCurlSnippet, siteGithubActionsSnippet, type SiteBuild } from './siteCiSnippets';

const target = { apiUrl: 'https://app.example.test/api/', projectId: 'proj-a', siteId: 'site-1' };

describe('site CI snippets', () => {
  test('post to the site deploy route with the token as a CI secret, never a value', () => {
    const snippet = siteCurlSnippet(target);
    expect(snippet).toContain(
      '"https://app.example.test/api/projects/proj-a/sites/site-1/deploy?label=$COMMIT_SHA"',
    );
    expect(snippet).toContain('-H "Authorization: Bearer $EXCALIBASE_TOKEN"');
    expect(snippet.match(/curl /g)).toHaveLength(1);
  });

  test.each<[string, SiteBuild]>([
    ['a climbing output', { output: '../secrets' }],
    ['an absolute output', { output: '/etc' }],
    ['the repository itself', { output: '.' }],
    ['the context itself', { context: 'apps/web', output: './' }],
    ['a climbing context', { context: '../other' }],
    ['a quote in the output', { output: 'dist"x' }],
    ['a space in the context', { context: 'apps/a b' }],
    ['a branch expression', { branch: '${{ github.event }}' }],
    ['a branch with a climb', { branch: 'a..b' }],
    ['a shell variable in paths', { output: '$HOME' }],
  ])('refuses %s', (_name, build) => {
    expect(() => siteGithubActionsSnippet({ ...target, build })).toThrow();
  });
});
