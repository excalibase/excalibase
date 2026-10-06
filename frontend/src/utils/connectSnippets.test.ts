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
    expect(code).toContain('key: process.env.EXCALIBASE_PUBLISHABLE_KEY');
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

  // EXC-555: a quoted table name such as "my table!" went into the URL raw; the
  // copied curl line then printed nothing.
  test('a table name is percent-encoded in the REST path', () => {
    expect(sdkSnippet(base, project, 'my table!')).toContain("db.rest.get('/my%20table!?limit=10')");
    expect(curlSnippet(base, project, 'my table!')).toContain('/api/v1/my%20table!?limit=10');
    expect(curlSnippet(base, project, "it's")).not.toContain("it's");
  });
});
