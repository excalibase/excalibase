# API permissions (control plane)

The control plane stores a project's API permissions and serves them to the engine as one
document. Semantics (roles, session variables, how each surface applies a permission) are in the
engine's `docs/features/permissions.md`; this page covers the control-plane API.

## Engine read

`GET /api/provision/{projectId}/permissions/` — Developer, or a service token with `policies:read`.

- `404` for an unknown project. Every array is present, never `null`. There is no `enforced` flag.
- `version` goes up on every permission, tracked-function or function-permission write.

```json
{
  "projectId": "proj-abc",
  "version": 42,
  "tables": [
    { "table": "public.orders", "role": "user",
      "select": { "filter": {"owner_id": {"_eq": "X-Excalibase-User-Id"}}, "columns": "*", "allowAggregations": false } }
  ],
  "functions": [
    { "function": "public.search_orders", "exposedAs": "QUERY", "inferPermissions": true, "sessionArgument": null }
  ],
  "functionPermissions": [ { "function": "public.search_orders", "role": "editor" } ]
}
```

## Writes (Developer)

| Method | Path | Answer |
|---|---|---|
| PUT | `permissions/tables/{schema.table}/roles/{role}/{operation}` | 200, the stored (normalized) object |
| DELETE | `permissions/tables/{schema.table}/roles/{role}/{operation}` | 204, 404 if absent |
| POST | `tracked-functions/` `{"function","inferPermissions","sessionArgument"}` | 201 with `exposedAs` and `securityDefiner`; 400 with the reason if not trackable; 409 if already tracked |
| DELETE | `tracked-functions/{schema.function}` | 204 (its function permissions go with it), 404 if absent |
| PUT | `function-permissions/{schema.function}/roles/{role}` | 200, 400 if the function is not tracked |
| DELETE | `function-permissions/{schema.function}/roles/{role}` | 204, 404 if absent |

Every write publishes `policies.{projectId}.changed` (kind `permission` or `function`).

**Validation.** Table and function keys are `schema.name` in lower-case identifiers; roles match
`^[a-z][a-z0-9_]{0,62}$` and are never `service`. Each operation takes only its own keys — select:
`filter`, `columns`, `limit`, `allowAggregations`; insert: `check`, `columns`, `set`; update:
`filter`, `check`, `columns`, `set`; delete: `filter` (`filter`/`check` and `columns` required).
Expressions follow the Hasura grammar, at most 16 deep and 200 nodes. Column names are not checked
against the database; the engine refuses unknown columns.

**Tracking a function** reads its live definition: procedures, aggregates, overloaded names and
functions not returning rows of a served table or view are refused; `exposedAs` is `MUTATION` for
`VOLATILE`, else `QUERY`; a `sessionArgument` must be a `json`/`jsonb` argument.

## Legacy fold

At startup the control plane folds each project's table grants, row policies and column policies
into permissions once (`legacy_permissions_migrated` marks the project). It reads the project's
live tables, so a project whose database is down is retried at the next start. Existing
permissions are never replaced. Everything not carried over is logged per project:

- a row policy using a time variable, `{{currentUserGroupIds}}` or a JSON-path field fails closed —
  the permissions it would have shaped are not written;
- policy assignments to a single user or a group are dropped (spec §9);
- PARTIAL/HASH/CUSTOM masks were never applied, so those columns stay selectable.

The legacy tables and endpoints stay until the engine switches to the document.
