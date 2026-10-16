#!/usr/bin/env node
// Drives each example app through a fresh platform the way a builder and their coding tool do
// (EXC-611): a project, migrations applied back to back, permissions, keys, realtime, the app's
// image built and deployed by digest, then checks from inside the app's container, a browser CORS
// preflight from the app's own origin, and the example repo's Playwright journeys at its real URL.
//
// Environment (run.sh sets it per stack):
//   STUDIO_URL, API_URL      where Studio (/api, /mcp) and the data plane answer
//   ADMIN_USER, ADMIN_PASSWORD_FILE
//   EXAMPLES_DIR             checkouts of the example repos, one directory per example name
//   PUSH_REGISTRY            where this machine pushes images (e.g. localhost:5001)
//   PULL_REGISTRY            the same registry as the platform names it (default PUSH_REGISTRY)
//   APP_EXEC                 a command that runs `<app id> <argv...>` inside the app's container
//   ONLY                     optional comma list of example names
//   REPORT                   optional file for the per-example verdicts (JSON)
import { execFileSync, spawnSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { examples, migrationsOf, seedOf } from './examples.mjs';
import { eventually, mcp, signIn, studio } from './platform.mjs';

const env = (name, fallback) => {
  const value = process.env[name] ?? fallback;
  if (value === undefined || value === '') throw new Error(`${name} is required`);
  return value;
};
const STUDIO_URL = env('STUDIO_URL');
const API_URL = env('API_URL');
const EXAMPLES_DIR = env('EXAMPLES_DIR');
const PUSH_REGISTRY = env('PUSH_REGISTRY');
const PULL_REGISTRY = env('PULL_REGISTRY', PUSH_REGISTRY);
const APP_EXEC = env('APP_EXEC');
const run = randomBytes(3).toString('hex');

function log(example, message) {
  console.log(`[${example}] ${message}`);
}

function sh(command, args, options = {}) {
  const out = execFileSync(command, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'], ...options });
  return (out ?? '').trim();
}

/** Builds the example's image as its CI would and pushes it; the platform deploys that digest. */
function buildAndPush(example, dir) {
  const tag = `${PUSH_REGISTRY}/${example.name}:gate-${run}`;
  sh('docker', ['build', '-q', '-t', tag, dir], { stdio: ['ignore', 'pipe', 'inherit'] });
  const pushed = sh('docker', ['push', tag]);
  const digest = pushed.match(/digest: (sha256:[0-9a-f]{64})/)?.[1];
  if (!digest) throw new Error(`push of ${tag} named no digest: ${pushed.slice(-300)}`);
  return `${PULL_REGISTRY}/${example.name}@${digest}`;
}

async function newProject(api, orgId, name) {
  const created = await api.post('/provision/', { projectName: name, orgId, databaseType: 'POSTGRESQL', postgresVersion: '17' });
  const projectId = created.projectId;
  await eventually(`project ${name} ACTIVE`, async () => {
    const state = await api.get(`/provision/${projectId}/`);
    if (state.status === 'FAILED') throw Object.assign(new Error(`project failed: ${state.failureReason}`), { fatal: true });
    return state.status === 'ACTIVE' ? state : null;
  }, { tries: 120, every: 5000 });
  return projectId;
}

/** An end user of the project, signed up through its auth API with the publishable key. */
async function endUser(ctx, name) {
  const email = `gate-${name}-${run}@example.com`;
  const password = randomBytes(18).toString('base64url');
  const answer = await fetch(`${ctx.apiUrl}/auth/${ctx.projectId}/${ctx.projectId}/register`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Excalibase-Publishable-Key': ctx.publishableKey },
    body: JSON.stringify({ email, password, fullName: `Gate ${name}` }),
  });
  const body = await answer.json().catch(() => ({}));
  if (!answer.ok || !(body.user?.id > 0)) throw new Error(`sign-up of ${email}: ${answer.status} ${JSON.stringify(body).slice(0, 300)}`);
  return { id: body.user.id, email, password };
}

const publishableKeyIn = (answer) => JSON.stringify(answer).match(/esk_pub_[A-Za-z0-9_]+/)?.[0];

async function setUpData(example, dir, tool) {
  // Back to back, as a coding tool applies them (EXC-605): every one must be in the history.
  const migrations = migrationsOf(example, dir);
  for (const { path, sql } of migrations) await tool('apply_migration', { name: path.replace(/^.*\//, ''), sql });
  const history = JSON.stringify(await tool('list_migrations'));
  for (const { path } of migrations) {
    if (!history.includes(path.replace(/^.*\//, ''))) throw new Error(`migration ${path} missing from the history`);
  }
  const seed = seedOf(example, dir);
  if (seed) await tool('execute_sql', { query: seed });
  for (const rule of example.permissions(dir)) await tool('set_permission', rule);
  for (const fn of example.functions) await tool('track_db_function', fn);
  for (const table of example.realtime) await tool('set_realtime', { table, enabled: true });
}

async function deploy(example, ctx, image) {
  const created = await ctx.tool('create_app', {
    name: example.name, image, port: example.app.port, health_check_path: example.app.healthCheckPath,
  });
  const app = created.app;
  if (!app?.url?.startsWith(`${ctx.appScheme}//`)) throw new Error(`create_app answered ${app?.url}, not an ${ctx.appScheme} URL`);
  if (created.corsOriginAdded !== app.url) throw new Error(`create_app added ${created.corsOriginAdded} to CORS, not ${app.url}`);
  const setEnv = Object.entries(example.env(ctx)).map(([name, value]) => ({ name, value }));
  await ctx.tool('update_app', { app_id: app.id, set_env: setEnv, database_env: example.app.databaseEnv });
  // No image: deploy_app runs the digest the app names, as create_app's answer tells the tool to.
  await ctx.tool('deploy_app', { app_id: app.id });
  // get_deploy_status probes the URL the way a user opens it; "succeeded" means it answered (EXC-608).
  await eventually(`${example.name} deployed`, async () => {
    const status = await ctx.tool('get_deploy_status', { app_id: app.id });
    if (status.state === 'failed') throw Object.assign(new Error(`deploy failed: ${JSON.stringify(status).slice(0, 600)}`), { fatal: true });
    if (status.state !== 'succeeded') throw new Error(`state ${status.state}: ${JSON.stringify(status.urlProbe ?? status.failureHint ?? status.deploys?.[0] ?? status).slice(0, 500)}`);
    return status;
  }, { tries: 120, every: 5000 });
  return app;
}

/** The app's own server calls the API by its public name, as the example's server-side code does. */
function callFromInside(ctx, app) {
  const script = `fetch(process.argv[1],{method:"POST",headers:{"content-type":"application/json"},body:JSON.stringify({query:"{__typename}"})})`
    + `.then(r=>r.text().then(t=>console.log(r.status,t))).catch(e=>console.log("ERR",(e.cause&&(e.cause.code||e.cause.message))||e.message))`;
  const out = spawnSync(APP_EXEC, [app.id, 'node', '-e', script, `${ctx.apiUrl}/${ctx.projectId}/graphql`], { encoding: 'utf8' });
  const said = `${out.stdout}${out.stderr}`.trim();
  if (!/^200 .*__typename/.test(said)) throw new Error(`in-container call to ${ctx.apiUrl}: ${said.slice(0, 300)}`);
}

/** What a browser on the app's page sends before a credentialed GraphQL call. */
async function preflightFrom(ctx, origin) {
  await eventually(`CORS preflight from ${origin}`, async () => {
    const answer = await fetch(`${ctx.apiUrl}/${ctx.projectId}/graphql`, {
      method: 'OPTIONS',
      headers: { Origin: origin, 'Access-Control-Request-Method': 'POST', 'Access-Control-Request-Headers': 'authorization,content-type' },
    });
    const allowed = answer.headers.get('access-control-allow-origin');
    if (allowed !== origin) throw new Error(`${answer.status} allow-origin=${allowed}`);
    return allowed;
  }, { tries: 40, every: 3000 });
}

function journeys(example, dir, url, env) {
  sh('npm', ['ci', '--no-audit', '--no-fund'], { cwd: dir, stdio: ['ignore', 'ignore', 'inherit'] });
  sh('npx', ['playwright', 'install', 'chromium'], { cwd: dir, stdio: ['ignore', 'ignore', 'inherit'] });
  const result = spawnSync('npx', ['playwright', 'test', '--reporter=line'], {
    cwd: dir,
    stdio: 'inherit',
    env: { ...process.env, ...env, E2E_BASE_URL: url, E2E_IGNORE_HTTPS_ERRORS: '1' },
  });
  if (result.status !== 0) throw new Error(`${example.name}'s Playwright journeys failed`);
}

async function walk(example, api, orgId) {
  const dir = join(EXAMPLES_DIR, example.name);
  const projectId = await newProject(api, orgId, `${example.name}-${run}`);
  log(example.name, `project ${projectId}`);
  const pat = await api.post('/auth/tokens/', { name: `gate-${example.name}`, projectId, scopes: ['read', 'write'] });
  const tool = mcp(`${STUDIO_URL}/mcp?project=${projectId}`, pat.token);
  const ctx = { projectId, apiUrl: API_URL, appScheme: env('APP_SCHEME', 'https:'), tool };
  ctx.endUser = (name) => endUser(ctx, name);

  await setUpData(example, dir, tool);
  ctx.publishableKey = publishableKeyIn(await tool('create_publishable_key', { name: `gate-${run}` }));
  if (!ctx.publishableKey) throw new Error('create_publishable_key returned no key');
  log(example.name, 'schema, permissions, key and realtime set through MCP');

  const image = buildAndPush(example, dir);
  const app = await deploy(example, ctx, image);
  log(example.name, `deployed ${image} at ${app.url}`);
  callFromInside(ctx, app);
  log(example.name, `the app's server reaches ${ctx.apiUrl}`);
  await preflightFrom(ctx, new URL(app.url).origin);
  log(example.name, 'a browser preflight from the app origin is allowed');

  const extra = example.prepare ? await example.prepare(ctx) : {};
  journeys(example, dir, app.url, { ...example.journeys(ctx), ...extra });
  log(example.name, 'Playwright journeys passed');
}

async function main() {
  const password = readFileSync(env('ADMIN_PASSWORD_FILE'), 'utf8').trim();
  const api = studio(STUDIO_URL, await signIn(STUDIO_URL, env('ADMIN_USER', 'admin'), password));
  const orgs = await api.get('/orgs');
  const orgId = (Array.isArray(orgs) ? orgs : orgs.orgs ?? orgs.items)[0].id;
  const only = process.env.ONLY ? process.env.ONLY.split(',') : null;
  const verdicts = {};
  for (const example of examples.filter((e) => !only || only.includes(e.name))) {
    try {
      await walk(example, api, orgId);
      // A gap the platform under test is known to have is reported with the pass, never dropped.
      const gaps = (process.env.E2E_KNOWN_GAPS ?? '').split(',').filter((gap) => gap === 'realtime' && example.realtime.length);
      verdicts[example.name] = gaps.length ? `passed (known gap: ${gaps.join(', ')})` : 'passed';
    } catch (error) {
      verdicts[example.name] = `failed: ${error.message}`;
      log(example.name, `FAILED ${error.message}`);
    }
  }
  if (process.env.REPORT) writeFileSync(process.env.REPORT, JSON.stringify(verdicts, null, 2));
  console.log(JSON.stringify(verdicts, null, 2));
  process.exit(Object.values(verdicts).every((v) => v.startsWith('passed')) ? 0 : 1);
}

main().catch((error) => {
  console.error(error.message);
  process.exit(1);
});
