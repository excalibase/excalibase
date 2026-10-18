# Functions: mutations and scheduled work

Functions have no database handle: `ctx.db` was removed with the document
data plane in `@excalibase/server` 0.11.0, and any use of it throws
`ctx.db is not available: read and write your project's data through its
GraphQL or REST API with a project key`. A function reads and writes data by
calling the project's API with a project key it keeps as a function secret.

A mutation is therefore not a database transaction. What the runtime does
keep per mutation is its `ctx.scheduler` calls:

- **Top-level mutation**: its `runAfter`, `runAt` and `cancel` calls are held
  until the handler returns, then sent to the platform in order. A mutation
  that throws schedules nothing.
- **Nested `ctx.runMutation`** from a mutation: its calls join the outer
  mutation's and are sent, or dropped, with them.
- **Action** (and `httpAction`): calls are sent at once and stand whatever
  the action does next. An action's `ctx.runMutation` is a new top level.

If the platform does not record a successful mutation's scheduled calls, the
caller gets `500 { "error": "the mutation succeeded but its scheduled
functions were not recorded" }` instead of a success that scheduled nothing.
Mutations run once; the runtime never retries them.
