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
npm install @excalibase/server zod
```

`zod` is a peer dependency. `zod-to-json-schema` is bundled as a regular
dependency because the metadata it produces is part of the package output.

## Usage

```ts
import { z } from "zod";
import { query, mutation } from "@excalibase/server";

export const getUser = query({
  args: z.object({ id: z.string() }),
  handler: async (ctx, args) => {
    // ctx.db is provided by the runtime at request time.
    return { id: args.id };
  },
});

export const createUser = mutation({
  args: z.object({ name: z.string() }),
  handler: async (ctx, args) => {
    return { name: args.name };
  },
});
```

## Do not run locally

The wrappers return inert records of the form
`{ kind, args, handler, __metadata: { argsJsonSchema } }`. The `handler` is
stored, not invoked. The Excalibase Deno runtime is responsible for building
`ctx` per request and calling `handler(ctx, parsedArgs)`. Importing this
package in a Node.js process and "calling" a definition will not execute the
handler.

## License

MIT
