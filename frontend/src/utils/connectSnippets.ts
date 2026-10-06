// What a developer copies to reach a project from code. The paths mirror the
// SDK (@excalibase/sdk): it takes the platform's public base URL and the
// project id, and builds every service path itself. Auth's org segment
// defaults to the project id, which auth accepts.

export interface ConnectEndpoints {
  apiUrl: string;
  graphql: string;
  rest: string;
  token: string;
  functions: string;
}

export const sdkInstall = 'npm install @excalibase/sdk graphql-request';

const PLACEHOLDER_TABLE = 'your_table';

export function connectEndpoints(apiUrl: string, projectId: string): ConnectEndpoints {
  const base = apiUrl.replace(/\/+$/, '');
  return {
    apiUrl: base,
    graphql: `${base}/${projectId}/graphql`,
    rest: `${base}/${projectId}/api/v1`,
    token: `${base}/auth/${projectId}/${projectId}/token`,
    functions: `${base}/functions/v1/${projectId}`,
  };
}

export function sdkSnippet(apiUrl: string, projectId: string, table: string = PLACEHOLDER_TABLE): string {
  const { apiUrl: base } = connectEndpoints(apiUrl, projectId);
  return `import { createClient } from '@excalibase/sdk';

const db = createClient({
  url: '${base}',
  projectId: '${projectId}',
  // A publishable key (esk_pub_...) from API Keys; safe in browser code.
  key: process.env.EXCALIBASE_PUBLISHABLE_KEY,
});

await db.auth.signInWithApiKey();
console.log(await db.graphql.query('{ __typename }'));
// A table answers once its API permissions let this role read it.
console.log(await db.rest.get('/${table}?limit=10'));`;
}

export function curlSnippet(apiUrl: string, projectId: string, table: string = PLACEHOLDER_TABLE): string {
  const urls = connectEndpoints(apiUrl, projectId);
  return `KEY='esk_pub_...'   # a publishable key from API Keys
TOKEN=$(curl -s -X POST '${urls.token}' \\
  -H 'Content-Type: application/json' \\
  -d "{\\"grant_type\\":\\"api_key\\",\\"api_key\\":\\"$KEY\\"}" | jq -r .accessToken)

# REST: one path per table. A table answers once its API permissions
# (Database > Tables > API permissions) let this role read it.
curl -s '${urls.rest}/${table}?limit=10' -H "Authorization: Bearer $TOKEN"

# GraphQL
curl -s -X POST '${urls.graphql}' -H "Authorization: Bearer $TOKEN" \\
  -H 'Content-Type: application/json' -d '{"query":"{ __typename }"}'`;
}
