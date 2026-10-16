// The example apps the gate builds, deploys and walks, set up the way their READMEs tell a coding
// tool to (EXC-611). Each repo is checked out at <dir>; its own Playwright journeys are the verdict.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const read = (dir, path) => readFileSync(join(dir, path), 'utf8');

/** lecture-search's db/permissions.json: { "<table>": { "<role>": { "<operation>": permission } } }. */
function nestedPermissions(dir) {
  const byTable = JSON.parse(read(dir, 'db/permissions.json'));
  return Object.entries(byTable)
    .filter(([table]) => !table.startsWith('_') && table !== 'functions')
    .flatMap(([table, roles]) =>
      Object.entries(roles).flatMap(([role, operations]) =>
        Object.entries(operations).map(([operation, permission]) => ({ table, role, operation, permission })),
      ),
    );
}

export const examples = [
  {
    name: 'lecture-search',
    repo: 'excalibase/example-lecture-search',
    migrations: ['db/0001_schema.sql', 'db/0002_search.sql'],
    seed: 'db/seed.sql',
    permissions: nestedPermissions,
    functions: [{ function: 'public.lecture_search', roles: ['anon', 'user'] }],
    realtime: [],
    app: { port: 8080, healthCheckPath: '/healthz' },
    env: (ctx) => ({
      EXCALIBASE_URL: ctx.apiUrl,
      EXCALIBASE_PROJECT_ID: ctx.projectId,
      EXCALIBASE_PUBLISHABLE_KEY: ctx.publishableKey,
    }),
    journeys: (ctx) => ({
      EXCALIBASE_URL: ctx.apiUrl,
      EXCALIBASE_PROJECT_ID: ctx.projectId,
      EXCALIBASE_PUBLISHABLE_KEY: ctx.publishableKey,
    }),
  },
  {
    name: 'live-sports',
    repo: 'excalibase/example-live-sports-dashboard',
    migrations: ['db/schema.sql'],
    seed: 'db/seed.sql',
    permissions: (dir) => JSON.parse(read(dir, 'db/permissions.json')).permissions,
    functions: [],
    realtime: ['sports_matches', 'sports_match_events'],
    app: { port: 8080, healthCheckPath: '/healthz' },
    env: (ctx) => ({
      EXCALIBASE_URL: ctx.apiUrl,
      EXCALIBASE_PROJECT_ID: ctx.projectId,
      EXCALIBASE_PUBLISHABLE_KEY: ctx.publishableKey,
    }),
    // The journey writes as a scorekeeper: an end user the operator allowlists, as the README says.
    prepare: async (ctx) => {
      const scorekeeper = await ctx.endUser('scorekeeper');
      await ctx.tool('execute_sql', {
        query: `insert into public.sports_scorekeepers (user_id) values (${Number(scorekeeper.id)})`,
      });
      return { E2E_SCOREKEEPER_EMAIL: scorekeeper.email, E2E_SCOREKEEPER_PASSWORD: scorekeeper.password };
    },
    journeys: (ctx) => ({
      EXCALIBASE_URL: ctx.apiUrl,
      EXCALIBASE_PROJECT_ID: ctx.projectId,
      EXCALIBASE_PUBLISHABLE_KEY: ctx.publishableKey,
    }),
  },
  {
    name: 'bookshelf',
    repo: 'excalibase/example-nestjs-api',
    migrations: ['db/migrations/0001_bookshelf.sql', 'db/migrations/0002_bookshelf_grants.sql'],
    permissions: () => [],
    functions: [],
    realtime: [],
    app: { port: 8080, healthCheckPath: '/healthz', databaseEnv: ['DATABASE_URL'] },
    env: (ctx) => ({
      EXCALIBASE_URL: ctx.apiUrl,
      EXCALIBASE_PROJECT_ID: ctx.projectId,
      TRUST_PROXY_HOPS: '1',
    }),
    journeys: (ctx) => ({
      EXCALIBASE_URL: ctx.apiUrl,
      EXCALIBASE_PROJECT_ID: ctx.projectId,
      EXCALIBASE_PUBLISHABLE_KEY: ctx.publishableKey,
    }),
  },
];

export const migrationsOf = (example, dir) => example.migrations.map((path) => ({ path, sql: read(dir, path) }));
export const seedOf = (example, dir) => (example.seed ? read(dir, example.seed) : '');
