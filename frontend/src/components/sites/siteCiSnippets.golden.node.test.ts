// @vitest-environment node
import { describe, expect, test } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import {
  siteCurlSnippet,
  siteGithubActionsSnippet,
  siteGitlabCiSnippet,
  siteJenkinsSnippet,
  type SiteSnippetTarget,
} from './siteCiSnippets';

// The server renders the same site pipelines for its MCP get_ci_snippet tool
// (shiptemplates.SiteCI); both sides are pinned to these files.
const GOLDEN_DIR = join(__dirname, '../../../../server-go/internal/shiptemplates/testdata');

const PROVIDERS: Record<string, (target: SiteSnippetTarget) => string> = {
  'github-actions': siteGithubActionsSnippet,
  'gitlab-ci': siteGitlabCiSnippet,
  jenkins: siteJenkinsSnippet,
  curl: siteCurlSnippet,
};

const target: SiteSnippetTarget = {
  apiUrl: 'https://app.example.test/api',
  projectId: 'proj-a',
  siteId: '0b8f2c1e-5d1a-4c7e-9f2a-3b4c5d6e7f80',
};

describe('site CI snippets match the server golden pipelines', () => {
  for (const [provider, render] of Object.entries(PROVIDERS)) {
    test(`${provider} building dist at the repository root`, () => {
      const golden = readFileSync(join(GOLDEN_DIR, `site-${provider}.golden`), 'utf8');
      expect(render(target)).toBe(golden);
    });

    // One frontend of a monorepo, built from its folder on a release branch.
    test(`${provider} with build options`, () => {
      const golden = readFileSync(join(GOLDEN_DIR, `site-${provider}.options.golden`), 'utf8');
      expect(
        render({
          ...target,
          build: { context: 'apps/web', output: 'build', branch: 'release/v2' },
        }),
      ).toBe(golden);
    });
  }
});
