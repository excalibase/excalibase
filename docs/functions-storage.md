# `ctx.storage` — Convex-shape file storage (Phase 10)

Excalibase Functions expose a Convex-parity `ctx.storage` surface on
every mutation, action, and httpAction handler. The shape mirrors
<https://docs.convex.dev/file-storage> one-for-one so code authored
against Convex docs ports without translation.

Storage is backed by Cloudflare R2 (S3-compatible) under the hood; the
runtime never proxies blob bytes through the worker pod when a direct
upload is possible. A per-project private bucket (`ctx-storage`) is
auto-provisioned on first use.

## Surface

```ts
interface StorageReader {
  getUrl(storageId: Id<"_storage">): Promise<string | null>;
  get(storageId: Id<"_storage">): Promise<Blob | null>;
  getMetadata(storageId: Id<"_storage">): Promise<StorageFileMetadata | null>;
}

interface StorageWriter extends StorageReader {
  generateUploadUrl(): Promise<string>;
  store(blob: Blob, opts?: { sha256?: string }): Promise<Id<"_storage">>;
  delete(storageId: Id<"_storage">): Promise<void>;
}

interface StorageFileMetadata {
  storageId: string;
  sha256: string;
  size: number;
  contentType?: string;
}
```

| Ctx kind     | Surface         | Methods                                                 |
|--------------|-----------------|---------------------------------------------------------|
| `QueryCtx`   | `StorageReader` | `getUrl`, `get`, `getMetadata`                          |
| `MutationCtx`| `StorageWriter` | + `generateUploadUrl`, `store`, `delete`                |
| `ActionCtx`  | `StorageWriter` | + `generateUploadUrl`, `store`, `delete`                |

Calling `ctx.storage.delete(...)` inside a query is a compile-time
error (the typed `StorageReader` does not expose it) and a clear
runtime error from a hand-rolled bundle that bypasses the typed lib
("method missing on QueryCtx.storage").

## Direct upload flow (recommended for browser clients)

The Convex direct-upload pattern keeps user blob bytes off the function
runtime. Bandwidth and CPU stay on the client → R2 path; provisioning
only mints signed URLs and records metadata.

### 1. Server: mint the signed PUT URL inside a mutation

```ts
import { mutation, v } from "@excalibase/server";

export const generateUploadUrl = mutation({
  args: v.object({}),
  handler: async (ctx) => {
    // ctx.storage.generateUploadUrl() returns a short-lived signed PUT
    // URL the client uploads to directly. Provisioning auto-creates
    // the per-project ctx-storage bucket on first call.
    return ctx.storage.generateUploadUrl();
  },
});
```

### 2. Server: attach the resulting `storageId` to a row

```ts
import { mutation, v } from "@excalibase/server";

export const sendImage = mutation({
  args: v.object({
    storageId: v.id("_storage"),
    author: v.string(),
  }),
  handler: async (ctx, { storageId, author }) => {
    await ctx.db.collection("messages").insert({
      author,
      image: storageId,
    });
  },
});
```

### 3. Client (excalibase-sdk-js)

```ts
import { db } from "./excalibase";

async function uploadAndAttach(blob: Blob, author: string) {
  // db.storage.uploadFile internally:
  //   a) calls api.<module>.generateUploadUrl to get the signed PUT URL,
  //   b) PUTs the blob bytes to that URL,
  //   c) reads the upload response and returns the minted storageId.
  const { storageId } = await db.storage.uploadFile(blob);

  // Pass the id to whatever mutation persists it on a row.
  await db.functions.messages.sendImage({ storageId, author });
}
```

The two mutations are decoupled on purpose: a single deployment can
ship many upload-attaching mutations that all reuse the same
`generateUploadUrl` helper.

## Server-side upload (actions)

When the function runtime itself produces the bytes (e.g. an action
that fetches an upstream URL and persists the response), use the
`store` helper:

```ts
import { action, v } from "@excalibase/server";

export const cacheUpstream = action({
  args: v.object({ url: v.string() }),
  handler: async (ctx, { url }) => {
    const upstream = await fetch(url);
    const blob = await upstream.blob();
    const id = await ctx.storage.store(blob);
    return { storageId: id };
  },
});
```

`store` opens a signed PUT URL internally, streams the bytes to R2,
records metadata, and returns the new `Id<"_storage">`.

## Reading

`ctx.storage.getUrl(id)` returns a short-lived signed download URL the
client can fetch directly. Render images, video, etc. from query
results without proxying through the function runtime.

```ts
import { query } from "@excalibase/server";

export const recentMessages = query({
  args: v.object({}),
  handler: async (ctx) => {
    const msgs = await ctx.db.collection("messages").find();
    return Promise.all(
      msgs.map(async (m) => ({
        ...m,
        url: await ctx.storage.getUrl(m.image as never),
      })),
    );
  },
});
```

Use `ctx.storage.get(id)` only when the runtime itself needs the
bytes (parsing CSV, scanning images, transcoding, etc.). For typical
client-rendering flows prefer `getUrl`.

## Deleting

`ctx.storage.delete(id)` is idempotent — calling it on a missing id is
a no-op. Use it from a mutation whenever you remove the row that
referenced the blob:

```ts
export const deleteMessage = mutation({
  args: v.object({ id: v.id("messages") }),
  handler: async (ctx, { id }) => {
    const msg = await ctx.db.collection("messages").getById(id);
    if (!msg) return;
    await ctx.db.collection("messages").delete({ _id: id });
    if (msg.image) await ctx.storage.delete(msg.image as never);
  },
});
```

## Wire contract

Internal routes the Deno runtime calls (auth: `X-Excalibase-Runtime-Token`):

| Method   | Path                                                  | Description                                |
|----------|-------------------------------------------------------|--------------------------------------------|
| `POST`   | `/internal/storage/{projectId}/upload-url`            | mint signed PUT URL + storageId            |
| `POST`   | `/internal/storage/{projectId}/confirm-upload`        | record metadata after PUT completes        |
| `POST`   | `/internal/storage/{projectId}/download-url`          | mint signed GET URL for an existing id     |
| `GET`    | `/internal/storage/{projectId}/metadata/{storageId}`  | fetch metadata; 404 on missing             |
| `DELETE` | `/internal/storage/{projectId}/{storageId}`           | remove the storage id; idempotent          |

The runtime caps every call with the shared `RUNTIME_SECRET`; user
code never holds these credentials. Public uploads still ride the
existing Supabase-shape `/api/projects/{projectId}/storage` routes
unchanged.

## Limits

* Per-project quotas are enforced via the existing storage tier table
  (FREE / PRO / ENTERPRISE) on `upload-url`.
* Per-bucket file-size caps are not exposed through `ctx.storage`
  (the auto-provisioned `ctx-storage` bucket has no per-file cap).
* Streaming/multipart uploads of very large files (`> 100 MB`) land
  in Phase 10.1; the current `store` round-trips the bytes through
  the runtime pod, which adds a memory hop. For large blobs prefer
  `generateUploadUrl` + direct client PUT.
