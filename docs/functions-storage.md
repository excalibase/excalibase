# `ctx.storage` — Convex-shape file storage (Phase 10)

Excalibase Functions expose a Convex-parity `ctx.storage` surface on
every mutation, action, and httpAction handler. The shape mirrors
<https://docs.convex.dev/file-storage> one-for-one so code authored
against Convex docs ports without translation.

Storage is backed by Cloudflare R2 (S3-compatible) under the hood; the
runtime never proxies blob bytes through the worker pod when a direct
upload is possible. A per-project private bucket (`ctx-storage`) is
auto-provisioned on first use. Files live in a bucket of their own, apart
from backups (`STORAGE_*`, EXC-560).

## App uploads without functions (EXC-560, feature `appstorage`)

Most apps need no upload function at all. A bucket carries **access rules**
for the project's app users, by the role in their token, and the SDK calls
the platform's own storage API with the signed-in user's token:

```ts
const user = db.auth.user(); // after db.auth.signInWithPassword(...)
await db.storage.uploadFile(file, { bucket: "avatars", path: `${user!.id}/avatar.png` });
const blob = await db.storage.download("avatars", `${user!.id}/avatar.png`);
const page = await db.storage.list("avatars");          // the user's own folder
await db.storage.remove("avatars", `${user!.id}/avatar.png`);
```

Rules are set in Studio (Storage → bucket → App access) or with
`PUT /api/projects/{projectId}/storage/buckets/{bucket}/access`:

```json
{"access": {"authenticated": {"read": "own", "write": "own", "delete": "own"},
            "staff": {"write": "all", "delete": "all"}}}
```

* `own` reaches keys under `<userId>/`, the signed-in user's id; `all` every
  key; leaving an operation out grants nothing. A role with no rule gets
  nothing, and an unknown bucket is refused exactly like one without a rule.
* An api-key token (the publishable key's) names no user, so `own` gives it
  nothing: every visitor holds the same token.
* A public bucket is readable by anyone; listing still needs a `read` rule.
* `X-Excalibase-Role` picks another role the token lists in `allowed_roles`.
* Bucket limits (size, MIME allowlist, project quota) apply as for Studio.

The routes, all `Authorization: Bearer <project access token>`, answer CORS
from the project's allowlist (`project-cors.md`):

| Method | Path under `/storage/v1/{projectId}/buckets/{bucket}` | Rule |
|---|---|---|
| `POST` | `/upload-url` `{key, mimeType, size}` | write |
| `POST` | `/confirm-upload` `{key, uploadId}` | write |
| `GET` | `/objects?prefix=&limit=&cursor=` | read (`own` lists the caller's folder) |
| `GET` | `/download-url/{key}` | read |
| `DELETE` | `/objects/{key}` | delete |

The bytes go between the browser and the object store on signed URLs; the
file bucket's CORS rule is in the production runbook §2.1. Use the function
flow below when the upload rule needs code (checking a row, a quota of your
own, attaching the file to a record in the same call).

## Surface

```ts
interface StorageReader {
  getUrl(storageId: Id<"_storage">): Promise<string | null>;
  get(storageId: Id<"_storage">): Promise<Blob | null>;
  getMetadata(storageId: Id<"_storage">): Promise<StorageFileMetadata | null>;
}

interface StorageWriter extends StorageReader {
  generateUploadUrl(d: { contentType: string; size: number }):
    Promise<{ url: string; storageId: string; uploadId: string }>;
  completeUpload(c: { storageId: string; uploadId: string }): Promise<Id<"_storage">>;
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
| `MutationCtx`| `StorageWriter` | + `generateUploadUrl`, `completeUpload`, `store`, `delete` |
| `ActionCtx`  | `StorageWriter` | + `generateUploadUrl`, `completeUpload`, `store`, `delete` |

Calling `ctx.storage.delete(...)` inside a query is a compile-time
error (the typed `StorageReader` does not expose it) and a clear
runtime error from a hand-rolled bundle that bypasses the typed lib
("method missing on QueryCtx.storage").

## Direct upload flow (recommended for browser clients)

The Convex direct-upload pattern keeps user blob bytes off the function
runtime. Bandwidth and CPU stay on the client → R2 path; provisioning
only mints signed URLs and records metadata.

The bytes do not land on the object's own key. They land on a staging key of
the upload's own, and are copied across only once the platform has read them
back and accepted their size and type. An upload that turns out to break the
bucket's limits is refused without touching whatever is already stored under
that key. The upload URL therefore comes with an `uploadId`, and the
confirmation names it:

```
POST .../storage/buckets/{bucket}/upload-url
  {"key": "avatars/1.png", "mimeType": "image/png", "size": 12345}
  → {"uploadId": "upl_…", "url": "https://…/.staging/upl_…", "method": "PUT",
     "headers": {"Content-Type": "image/png", "Content-Length": "12345"},
     "expiresAt": "…"}

PUT <url>  (exactly those headers, exactly that many bytes)

POST .../storage/buckets/{bucket}/confirm-upload
  {"key": "avatars/1.png", "uploadId": "upl_…"}
```

`size` and `mimeType` are required when the URL is minted — both are bound
into the signature — and are **not** accepted on confirm: what is recorded and
charged is what the object store reports. A confirmation that names an upload
nobody staged, or one whose bytes have already been collected, answers `404`.

### Who may upload: the function decides

`ctx.storage` trusts the function that calls it; the function is the
project's storage rule. Check the caller in it. `ctx.auth.claims` holds the
caller's token only when the platform verified it (functions with
`verifyJwt`, the default); for a function that skips verification it is
`null`, so a role check there cannot be satisfied by an unsigned token.
Refuse with `FunctionError` so the caller gets `401`/`403`; any other thrown
error answers a bare `500`.

The request a function sees never carries a platform credential: the
gateway removes the Studio cookies (`excali_session`, `excali_oauth_state`)
by name, an `Authorization: Bearer excb_…` platform token, and its own
internal headers. Your app's own cookies and tokens pass through unchanged.

```ts
export const generateUploadUrl = mutation({
  args: v.object({ contentType: v.string(), size: v.number() }),
  handler: async (ctx, { contentType, size }) => {
    if (!ctx.auth.claims) throw new FunctionError(401, "sign in to upload");
    if (ctx.auth.claims.role !== "staff") throw new FunctionError(403, "only staff may upload");
    return ctx.storage.generateUploadUrl({ contentType, size });
  },
});
```

Function ids are `module.export`; the export may be camelCase, so the
defaults `db.storage.uploadViaFunctions` calls, `system.generateUploadUrl` and
`system.completeUpload`, are deployable as they are. A browser app calls them
from an origin on the project's CORS allowlist (see `project-cors.md`).
`@excalibase/server` and `zod` may be imported by bare name, as below; the
bundler points them at the copies the runtime image carries.

### 1. Server: mint the signed PUT URL inside a mutation

```ts
import { mutation, v } from "@excalibase/server";

export const generateUploadUrl = mutation({
  // The client says what it is about to send: both are bound into the
  // signature, so neither can be decided later.
  args: v.object({ contentType: v.string(), size: v.number() }),
  handler: async (ctx, { contentType, size }) => {
    // Returns { url, storageId, uploadId }. The bytes land on a staging
    // key; the object appears when completeUpload accepts them.
    // Provisioning auto-creates the per-project ctx-storage bucket on
    // first call.
    return ctx.storage.generateUploadUrl({ contentType, size });
  },
});

// The follow-up mutation accepts the upload and attaches the id to a row.
// Without it the bytes are never recorded, and the platform collects them
// after its grace period.
export const attachUpload = mutation({
  args: v.object({ storageId: v.string(), uploadId: v.string() }),
  handler: async (ctx, { storageId, uploadId }) => {
    await ctx.storage.completeUpload({ storageId, uploadId });
    return storageId;
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
  // The upload is three steps, because the bytes are staged before they
  // become an object:
  //   a) call the mutation that mints the URL, declaring the blob's type
  //      and size — both are bound into the signature;
  //   b) PUT the blob with exactly that Content-Type and Content-Length;
  //   c) call the mutation that accepts the upload by its uploadId.
  const { data: minted } = await db.functions.system.generateUploadUrl({
    contentType: blob.type || "application/octet-stream",
    size: blob.size,
  });
  await fetch(minted.url, {
    method: "PUT",
    headers: {
      "Content-Type": blob.type || "application/octet-stream",
      "Content-Length": String(blob.size),
    },
    body: blob,
  });
  await db.functions.system.attachUpload({
    storageId: minted.storageId,
    uploadId: minted.uploadId,
  });

  // Pass the id to whatever mutation persists it on a row.
  await db.functions.messages.sendImage({ storageId: minted.storageId, author });
}
```

The mutations are decoupled on purpose: a single deployment can ship many
upload-attaching mutations that all reuse the same `generateUploadUrl`
helper. An upload that is never accepted is collected after the platform's
grace period, so step (c) is part of the flow, not an optimisation.

`db.storage.uploadViaFunctions(blob)` in `excalibase-sdk-js` runs these three
steps in one call (it was `uploadFile` before 0.12.0, which now uploads
through the bucket API above). By default it calls `system.generateUploadUrl`
and `system.completeUpload`; `opts.ref` and `opts.completeRef` name other
mutations. It returns the `storageId` only after the completion succeeds.

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

The runtime signs every call with its project's runtime token (derived from
the platform secret, valid for that project only); user code never holds it.
On Kubernetes each project's runtime reaches provisioning's internal listener
at `DENO_PROVISIONING_URL` (provisioning refuses to start without it when it
runs functions there). Studio uploads ride `/api/projects/{projectId}/storage`
(platform members); app users ride `/storage/v1/{projectId}` (above).

## Limits

* Per-project quotas are enforced via the existing storage tier table
  (FREE / PRO / ENTERPRISE) on `upload-url`, and again on confirm against
  what the object store actually holds.
* Per-bucket file-size caps are not exposed through `ctx.storage`
  (the auto-provisioned `ctx-storage` bucket has no per-file cap).
* Streaming/multipart uploads of very large files (`> 100 MB`) land
  in Phase 10.1; the current `store` round-trips the bytes through
  the runtime pod, which adds a memory hop. For large blobs prefer
  `generateUploadUrl` + direct client PUT.
