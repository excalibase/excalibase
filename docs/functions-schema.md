# Functions: schema declaration

Edge functions can declare a typed schema for the project's NoSQL store using
the Convex-shape `defineSchema` API from `@excalibase/server`. The bundler
captures the schema at deploy time, persists it on the function record, and
applies it to the project's Postgres instance as additive DDL.

## Declaring a schema

Add a `schema.ts` (or any module reachable from `index.ts`) to your function
bundle and call `defineSchema`:

```ts
import { defineSchema, defineTable, v } from "@excalibase/server";

export default defineSchema({
  users: defineTable(
    v.object({
      name: v.string(),
      email: v.string(),
      age: v.optional(v.number()),
    }),
  )
    .index("by_email", ["email"])
    .searchIndex("by_name", { searchField: "name" }),
  posts: defineTable(
    v.object({
      title: v.string(),
      body: v.string(),
      authorId: v.id("users"),
      embedding: v.array(v.number()),
    }),
  )
    .index("by_author", ["authorId"])
    .vectorIndex("by_embedding", {
      vectorField: "embedding",
      dimensions: 1536,
    }),
});
```

System fields `_id` (text) and `_creationTime` (double) are auto-injected on
every table. You do not declare them.

## Migration rules (v1)

The migrator only adds. It will:

- `CREATE TABLE IF NOT EXISTS nosql.<table>` for any new table.
- `CREATE INDEX IF NOT EXISTS` for any new btree, search, or vector index.
- `ALTER TABLE ADD COLUMN IF NOT EXISTS` for any new generated search
  column or vector embedding column.

The migrator will not:

- DROP a table, column, or index that no longer appears in the schema.
- ALTER an existing column type or constraint.
- Rename tables or fields.

If you need a destructive change, perform it manually in the project
database via the Studio or `psql`. A future phase will surface schema-diff
warnings and a guarded path for ALTER-style migrations.

## When the migration runs

By default (`EXCALIBASE_AUTO_MIGRATE=true`), the migration runs inline with
the function deploy. If the migration fails, the deploy fails atomically —
the function row is rolled back, the bundle is not deployed to the runtime.

To defer migration to an explicit admin call, set
`EXCALIBASE_AUTO_MIGRATE=false`. The deploy then stores the schema on the
function record without applying it; you can apply it later with:

```bash
curl -X POST \
  -H "Authorization: Bearer $PAT" \
  $EXCALIBASE_URL/api/projects/$PROJECT_ID/schema/apply
```

The endpoint walks every function with a stored `schemaJson` and runs
`ApplySchema` for it. Idempotent — safe to call repeatedly.
