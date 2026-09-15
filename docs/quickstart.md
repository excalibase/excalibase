# Quickstart

Five minutes from zero to a working API.

You need: an excalibase deployment with Studio reachable in your browser.
If you're following a beta invite, the operator gave you a URL.

## 1. Sign up

Open Studio, click **Sign up**, enter email and password.

The account you just made is your **operator account**. It owns orgs and
projects. It is not the same thing as your app's end-users.

## 2. Create an org

From the home page, **New org**. Pick a slug like `acme`. Orgs are how
projects are grouped: billing, members, and vault credentials all hang
off the org.

## 3. Create a project

Inside the org, **New project**.

* Name: anything, e.g. `todos-prod`
* Database: PostgreSQL
* Tier: STANDARD

Hit **Create**. Wait for the status to flip to **Active**. That takes
about 60 seconds: a Postgres pod, a backup job, and the API gateway
need to come up.

## 4. Find your URLs

Open the project, go to **API**. You get five URLs. They look like this:

```
REST     http://<host>:3000
GraphQL  http://<host>:4000/graphql
Auth     http://<host>:24000/auth/<projectId>
DB       postgresql://excalibase_app@<host>:5432/app
Edge fn  http://<host>:8000
```

Copy them. There is no "publishable key" today; auth is JWT only,
issued by the auth service when an end-user logs in. (Publishable keys
are planned and will land in a later release.)

## 5. Make a table

Two ways. Pick whichever is faster for you.

**Studio.** Go to **Table editor**, **New table**, add columns, save.

**psql.** Use the DB URL from step 4:

```bash
psql 'postgresql://excalibase_app@<host>:5432/app' -c "
create table todos (
  id         bigserial primary key,
  user_id    uuid not null,
  body       text not null,
  done       bool not null default false,
  created_at timestamptz not null default now()
);
"
```

Both REST and GraphQL pick the new table up within a few seconds. No
restart, no codegen.

## 6. Register an end-user

Your app's users are separate from your operator account.

```bash
curl -X POST http://<host>:24000/auth/<projectId>/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"alice@example.com","password":"hunter2","fullName":"Alice"}'
```

You get back:

```json
{ "token": "eyJhbGciOi...", "refreshToken": "...", "user": { ... } }
```

`token` is the JWT. Send it on every request as
`Authorization: Bearer <token>`.

## 7. First query

REST:

```bash
curl -H "Authorization: Bearer $TOKEN" \
  'http://<host>:3000/todos?select=id,body,done&limit=10'
```

GraphQL:

```bash
curl -X POST http://<host>:4000/graphql \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ todos(limit:10){ id body done } }"}'
```

Empty array on the first call is fine. The user has no rows yet.

## Where to go next

* **Row-level security**: open the **RLS** tab and add a policy.
  The JWT's `sub` claim is exposed to Postgres as `request.user_id`,
  so the usual filter is
  `using (user_id = current_setting('request.user_id', true))`.
* **Edge functions**: write a Deno handler, deploy it with `curl`,
  invoke it at `/functions/v1/<projectId>/<fnId>`.
  See [functions-query.md](functions-query.md). Deployed functions
  survive runtime restarts — see
  [functions-runtime-replay.md](functions-runtime-replay.md).
* **Realtime**: open a WebSocket to `ws://<host>:3000/ws` and
  subscribe to a table.
* **Backups**: nightly automatic. On-demand from the **Backups** tab.

## When things break

* **Project stuck in Provisioning.** Check
  `kubectl get pods -n <orgId>-<projectId>`. Usually the Postgres pod
  is pulling an image, or the PVC has not bound yet.
* **`401 unauthorized` from REST or GraphQL.** Token expired (one
  hour by default). POST `/auth/<projectId>/refresh` with the
  `refreshToken` you got at registration.
* **GraphQL schema looks empty.** The DB has no tables yet, or the
  schema cache has not refreshed. Add a table, wait five seconds.
* **`createMany` returns "permission denied".** RLS is enabled but
  the role granted to `excalibase_app` does not have INSERT on the
  table. Grant it.

That's it. Beta feedback goes to the email on your invite, or open an
issue in the repo. Tell us what hurt.
