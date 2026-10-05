// @vitest-environment node
import { describe, expect, test } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { curlSnippet, githubActionsSnippet, gitlabCiSnippet, jenkinsSnippet, type SnippetTarget } from './ciSnippets';

// The server renders the same pipelines for its MCP get_ci_snippet tool; both
// sides are pinned to these files, so Studio and the tool never drift apart.
const GOLDEN_DIR = join(__dirname, '../../../../server-go/internal/shiptemplates/testdata');

const IMAGES: Record<string, string> = {
  ghcr: 'ghcr.io/acme/web:main',
  dockerhub: 'acme/web',
  registry: 'registry.example.com/team/web@sha256:0123',
};

const PROVIDERS: Record<string, (target: SnippetTarget) => string> = {
  'github-actions': githubActionsSnippet,
  'gitlab-ci': gitlabCiSnippet,
  jenkins: jenkinsSnippet,
  curl: curlSnippet,
};

describe('CI snippets match the server golden pipelines', () => {
  for (const [label, image] of Object.entries(IMAGES)) {
    for (const [provider, render] of Object.entries(PROVIDERS)) {
      test(`${provider} for ${label}`, () => {
        const target = { apiUrl: 'https://app.example.test/api', projectId: 'proj-a', appId: 'web', image };
        const golden = readFileSync(join(GOLDEN_DIR, `${provider}.${label}.golden`), 'utf8');
        expect(render(target)).toBe(golden);
      });
    }
  }
});
