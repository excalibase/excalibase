# @excalibase/server

Server-side types and tagged wrappers for Excalibase Functions.

This package defines `query`, `mutation`, and `action` wrappers along with the
`Ctx` shape consumed by user-authored Excalibase Functions. The wrappers are
inert: they validate inputs via [zod](https://zod.dev) and attach a JSON Schema
copy of the arguments, but they do not execute anything by themselves. The
Excalibase Deno runtime constructs the request `Ctx` (database client, auth
claims, etc.) and invokes the stored handler on each request.

## Installation

```sh
npm install zod
```

There is nothing to install for a deploy: the package is not on npm, and the
function runtime carries both it and zod. Import them by bare name, as below,
or pinned (`npm:@excalibase/server@0.13.0`, `npm:zod@^3.22.0`); the platform's
bundler points bare names at the runtime's copies. Any other bare import must
be one of the function's own files, and an `npm:` package other than these two
is fetched from the registry, which a project runtime reaches only when its
egress allowlist says so; without that the deploy is refused and names the
package. `zod-to-json-schema` is bundled as a regular dependency
because the metadata it produces is part of the package output.

## Usage

Each function is one deployed module whose **default export** is the
wrapper; its id is `module.name`, and that is how a client calls it
(`db.functions.users.get(args)` → `POST /functions/v1/{projectId}/users.get`).

```ts
// deployed with id "users.get"
import { z } from "zod";
import { query } from "@excalibase/server";

export default query({
  args: z.object({ id: z.string() }),
  handler: async (ctx, args) => {
    // ctx.db is provided by the runtime at request time.
    return { id: args.id };
  },
});
```

```ts
// deployed with id "users.create"
import { z } from "zod";
import { mutation } from "@excalibase/server";

export default mutation({
  args: z.object({ name: z.string().min(1) }),
  handler: async (ctx, args) => {
    return { name: args.name };
  },
});
```

The runtime checks the arguments against `args` before the handler runs:
arguments the schema refuses answer `400 { "error": "args validation failed",
"issues": [...] }`, and the handler gets the parsed value.

## Refusing a caller

Throw `FunctionError` with a 4xx status to refuse the caller; the response is
that status with `{ "error": message }`. Any other thrown error is a crash:
the caller gets `500 { "error": "internal error" }` and the message is
written to the function's logs.

```ts
import { z } from "zod";
import { mutation, FunctionError } from "@excalibase/server";

// deployed with id "system.generateUploadUrl"
export default mutation({
  args: z.object({ contentType: z.string(), size: z.number() }),
  handler: async (ctx, args) => {
    if (!ctx.auth.claims) throw new FunctionError(401, "sign in to upload");
    if (ctx.auth.claims.role !== "staff") throw new FunctionError(403, "only staff may upload");
    return ctx.storage.generateUploadUrl(args);
  },
});
```

A refusal from a nested `ctx.runQuery` / `ctx.runMutation` / `ctx.runAction`
keeps its status if the caller lets it propagate. Arguments that fail the
`args` schema already answer `400`.

## Do not run locally

The wrappers return inert records of the form
`{ kind, args, handler, __metadata: { argsJsonSchema } }`. The `handler` is
stored, not invoked. The Excalibase Deno runtime is responsible for building
`ctx` per request and calling `handler(ctx, parsedArgs)`. Importing this
package in a Node.js process and "calling" a definition will not execute the
handler.

## License

MIT
