/**
 * `ctx.storage` type surface.
 *
 * The library declares the types; the Excalibase Deno runtime supplies
 * the concrete implementations by routing every call through the
 * main-thread RPC channel onto provisioning's R2-backed storage handler.
 *
 *   StorageReader.getUrl(id)        — signed read URL or null
 *   StorageReader.get(id)           — direct Blob (server-side)
 *   StorageReader.getMetadata(id)   — sha256 / size / contentType
 *
 *   StorageWriter extends StorageReader and adds:
 *   StorageWriter.generateUploadUrl() — signed PUT URL for client direct upload
 *   StorageWriter.store(blob, opts?)  — server-side upload, returns Id<"_storage">
 *   StorageWriter.delete(id)          — remove the object + metadata row
 *
 * Per-kind ctx wiring (see `./types.ts`):
 *   * QueryCtx.storage   : StorageReader  (read-only — queries cannot write)
 *   * MutationCtx.storage: StorageWriter
 *   * ActionCtx.storage  : StorageWriter
 *
 * The `_storage` brand tag is conventional: uploaded blobs are treated as
 * documents in a virtual `_storage` table and the typed handle reflects
 * that.
 */

/**
 * Storage IDs are opaque strings minted by the runtime when an upload is
 * confirmed. The brand is purely a TypeScript-time tag — at runtime an
 * `Id<"_storage">` is just a `string`. Kept inline because the lib no
 * longer ships a generic `Id<TableName>` brand.
 */
type Id<TBrand extends string> = string & { readonly __brand: TBrand };

/**
 * Stored-blob metadata. `StorageFileMetadata` shape:
 * sha256 is a hex-encoded 32-byte digest computed server-side at upload
 * time, `size` is in bytes, `contentType` is whatever the upload PUT
 * carried (omitted when the client uploaded with no `Content-Type`).
 */
export interface StorageFileMetadata {
  readonly storageId: string;
  readonly sha256: string;
  readonly size: number;
  readonly contentType?: string;
}

/**
 * Optional knobs accepted by `StorageWriter.store`. `sha256` lets the
 * caller pre-compute a checksum the runtime can verify against the
 * bytes it receives (defence against silently truncated multipart
 * uploads). Future fields can be added without a breaking change.
 */
export interface StorageStoreOptions {
  readonly sha256?: string;
}

/**
 * Read-only storage surface. Attached to `QueryCtx.storage`. Calling any
 * of these from a query is safe — none mutate state on the storage
 * backend or in the Postgres metadata table.
 *
 * `getUrl` returns a short-lived signed URL the client can fetch directly
 * (no proxy through the function runtime); `get` is for server-side reads
 * (e.g. parsing a CSV uploaded by the user); `getMetadata` exposes the
 * persisted record without downloading the bytes.
 */
export interface StorageReader {
  /**
   * Returns a signed download URL for the stored blob, or `null` when no
   * object with that id exists. The URL is short-lived (defaults to a
   * minute or two); cache the URL only for the request that produced it,
   * not across requests.
   */
  getUrl(storageId: Id<"_storage">): Promise<string | null>;

  /**
   * Returns the raw bytes as a `Blob`, or `null` when missing. Server-
   * side only — the worker pulls the bytes from provisioning over the
   * RPC channel. Avoid for large files; prefer `getUrl` and have the
   * client fetch directly.
   */
  get(storageId: Id<"_storage">): Promise<Blob | null>;

  /**
   * Returns the persisted metadata (sha256, size, contentType) or `null`
   * when no row exists. Cheap; no bytes are transferred.
   */
  getMetadata(storageId: Id<"_storage">): Promise<StorageFileMetadata | null>;
}

/**
 * Read + write storage surface. Attached to `MutationCtx.storage` and
 * `ActionCtx.storage`. Adds the three write operations:
 *
 *   * `generateUploadUrl()` — client-side direct-upload pattern.
 *     Returns a signed PUT URL the client uses to upload the bytes
 *     without proxying through the function runtime.
 *
 *   * `store(blob, opts?)` — server-side upload. Streams the bytes to
 *     the storage backend, persists the metadata row, returns the
 *     branded `Id<"_storage">`.
 *
 *   * `delete(id)` — remove both the object and the metadata row.
 *     Idempotent: deleting an unknown id is a no-op.
 */
export interface StorageWriter extends StorageReader {
  /**
   * Mint a signed PUT URL the client can upload to directly. Flow:
   *   1. Client invokes a mutation that calls `ctx.storage.generateUploadUrl()`.
   *   2. The mutation returns the URL to the client.
   *   3. The client PUTs the blob to that URL (no function-runtime
   *      bandwidth consumed).
   *   4. The upload response carries the new `storageId`, which the
   *      client passes to a follow-up mutation to attach to a row.
   */
  generateUploadUrl(): Promise<string>;

  /**
   * Stream a `Blob` to storage from the function runtime, returning the
   * branded id. Use when the runtime itself is producing the bytes (e.g.
   * an action that fetches an upstream URL and persists the response).
   */
  store(blob: Blob, opts?: StorageStoreOptions): Promise<Id<"_storage">>;

  /**
   * Remove the stored object and its metadata row. Idempotent — calling
   * with an unknown id resolves successfully without raising.
   */
  delete(storageId: Id<"_storage">): Promise<void>;
}
