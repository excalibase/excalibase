import { describe, test, expect } from 'vitest';
import { connectEndpoints, curlSnippet, sdkInstall, sdkSnippet } from './connectSnippets';

const base = 'https://api.example.test';
const project = 'proj-abc123';

describe('connect snippets', () => {
  test('endpoints are built from the public API base and the project id, as the SDK builds them', () => {
    expect(connectEndpoints(`${base}/`, project)).toEqual({
      apiUrl: base,
      graphql: `${base}/${project}/graphql`,
      rest: `${base}/${project}/api/v1`,
      token: `${base}/auth/${project}/${project}/token`,
      functions: `${base}/functions/v1/${project}`,
    });
  });

  test('the SDK snippet imports the published package and passes url, projectId and key', () => {
    const code = sdkSnippet(base, project, 'todos');
    expect(code).toContain("from '@excalibase/sdk'");
    expect(code).toContain(`url: '${base}'`);
    expect(code).toContain(`projectId: '${project}'`);
    expect(code).toContain("const PUBLISHABLE_KEY = 'esk_pub_...';");
    expect(code).toContain('key: PUBLISHABLE_KEY');
    expect(code).not.toContain('process.env');
    expect(code).toContain('await db.auth.signInWithApiKey();');
    expect(code).toContain("db.rest.get('/todos?limit=10')");
    // The broken snippet it replaces: wrong package, wrong option, org in the url.
    expect(code).not.toMatch(/@excalibase\/client|anonKey/);
    expect(sdkInstall).toBe('npm install @excalibase/sdk graphql-request');
  });

  test('the curl snippet exchanges the key for a token, then calls REST and GraphQL with it', () => {
    const sh = curlSnippet(base, project, 'todos');
    expect(sh).toContain(`curl -s -X POST '${base}/auth/${project}/${project}/token'`);
    expect(sh).toContain('\\"grant_type\\":\\"api_key\\"');
    expect(sh).toContain(`curl -s '${base}/${project}/api/v1/todos?limit=10' -H "Authorization: Bearer $TOKEN"`);
    expect(sh).toContain(`curl -s -X POST '${base}/${project}/graphql'`);
  });

  test('without a table yet, the examples name a placeholder table and say how to expose one', () => {
    expect(sdkSnippet(base, project)).toContain("db.rest.get('/your_table?limit=10')");
    expect(curlSnippet(base, project)).toContain('/api/v1/your_table?limit=10');
    expect(curlSnippet(base, project)).toMatch(/API permissions/);
  });

  // Byte for byte: a lost backslash breaks the copied shell command.
  test('the curl snippet is exactly the shell a user pastes', () => {
    expect(curlSnippet('https://api.example.test///', project, 'todos')).toBe(
      [
        "KEY='esk_pub_...'   # a publishable key from API Keys",
        "TOKEN=$(curl -s -X POST 'https://api.example.test/auth/proj-abc123/proj-abc123/token' \\",
        "  -H 'Content-Type: application/json' \\",
        '  -d "{\\"grant_type\\":\\"api_key\\",\\"api_key\\":\\"$KEY\\"}" | jq -r .accessToken)',
        '',
        '# REST: one path per table. A table answers once its API permissions',
        '# (Database > Tables > API permissions) let this role read it.',
        "curl -s 'https://api.example.test/proj-abc123/api/v1/todos?limit=10' -H \"Authorization: Bearer $TOKEN\"",
        '',
        '# GraphQL',
        "curl -s -X POST 'https://api.example.test/proj-abc123/graphql' -H \"Authorization: Bearer $TOKEN\" \\",
        "  -H 'Content-Type: application/json' -d '{\"query\":\"{ __typename }\"}'",
      ].join('\n'),
    );
  });

  // EXC-555: a quoted table name such as "my table!" went into the URL raw; the
  // copied curl line then printed nothing.
  test('a table name is percent-encoded in the REST path', () => {
    expect(sdkSnippet(base, project, 'my table!')).toContain("db.rest.get('/my%20table!?limit=10')");
    expect(curlSnippet(base, project, 'my table!')).toContain('/api/v1/my%20table!?limit=10');
    expect(curlSnippet(base, project, "it's")).not.toContain("it's");
  });
});
