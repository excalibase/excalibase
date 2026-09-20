// Phase 10 — worker-side ctx.storage facade.
//
// The worker is sandboxed (no net, no env, no fs). Every storage op posts
// an RPC message to the main thread, which translates it into an HTTP call
// against provisioning's `/internal/storage/{projectId}/*` routes,
// authenticated with the runtime-token shared secret.
//
// Two factories:
//   * `createStorageReader(rpc)` — produces a `StorageReader` for QueryCtx.
//     Methods: getUrl / get / getMetadata.
//   * `createStorageWriter(rpc)` — produces a `StorageWriter` for
//     MutationCtx / ActionCtx. Adds generateUploadUrl / store / delete.
//
// `rpc` is the seam: a single async function `(op, payload) => result` the
// caller wires to its postMessage transport. Keeps the facade pure so the
// unit tests can verify message shapes without spinning a worker.

/**
 * The minimal RPC contract the storage facade needs. Each op returns the
 * value the corresponding StorageReader/Writer method should resolve to:
 *
 *   getUrl           → string | null
 *   getMetadata      → StorageFileMetadata | null
 *   get              → { bytes: Uint8Array; contentType: string } | null
 *   generateUploadUrl → { url: string; storageId: string; uploadId: string }
 *   completeUpload   → string (the confirmed storageId)
 *   store            → string (the minted storageId)
 *   delete           → null  (200/204)
 *
 * Errors at the transport / provisioning side bubble out as rejected
 * promises so the runtime can surface them to the handler as throw.
 */
export type StorageRpc = (op: string, payload: unknown) => Promise<unknown>;

export interface StorageFileMetadata {
  storageId: string;
  sha256: string;
  size: number;
  contentType?: string;
}

export interface StorageReader {
  getUrl(storageId: string): Promise<string | null>;
  get(storageId: string): Promise<Blob | null>;
  getMetadata(storageId: string): Promise<StorageFileMetadata | null>;
}

/** What the caller must declare before a URL can be signed for it. */
export interface UploadDeclaration {
  contentType: string;
  size: number;
}

/** What a minted upload URL comes with. */
export interface MintedUpload {
  /** The signed PUT URL. It addresses a staging key, not the object's own. */
  url: string;
  /** The id the object will have once the upload is confirmed. */
  storageId: string;
  /** Names the staged bytes; `completeUpload` accepts them by this id. */
  uploadId: string;
}

/** Which staged upload to accept. */
export interface UploadCompletion {
  storageId: string;
  uploadId: string;
}

export interface StorageWriter extends StorageReader {
  generateUploadUrl(declaration: UploadDeclaration): Promise<MintedUpload>;
  completeUpload(completion: UploadCompletion): Promise<string>;
  store(blob: Blob, opts?: { sha256?: string }): Promise<string>;
  delete(storageId: string): Promise<void>;
}

function assertId(id: unknown, who: string): asserts id is string {
  if (typeof id !== "string" || id.length === 0) {
    throw new Error(`${who}: storageId must be a non-empty string`);
  }
}

/**
 * Build a read-only storage surface. Methods that target a missing id
 * resolve to `null` (signalled by the main thread returning a `null` body
 * — typically because provisioning replied 404).
 */
export function createStorageReader(rpc: StorageRpc): StorageReader {
  return {
    async getUrl(storageId) {
      assertId(storageId, "ctx.storage.getUrl");
      const reply = await rpc("getUrl", { storageId });
      if (reply === null || reply === undefined) return null;
      if (typeof reply !== "string") {
        throw new Error("ctx.storage.getUrl: unexpected RPC reply type");
      }
      return reply;
    },
    async get(storageId) {
      assertId(storageId, "ctx.storage.get");
      const reply = await rpc("get", { storageId });
      if (reply === null || reply === undefined) return null;
      if (
        typeof reply !== "object" ||
        !("bytes" in (reply as Record<string, unknown>))
      ) {
        throw new Error("ctx.storage.get: unexpected RPC reply type");
      }
      const r = reply as { bytes: Uint8Array; contentType?: string };
      // Re-wrap in a plain Uint8Array<ArrayBuffer> so Blob's strict BlobPart
      // type accepts it (newer TS rejects Uint8Array<ArrayBufferLike>).
      return new Blob([new Uint8Array(r.bytes)], {
        type: r.contentType || "application/octet-stream",
      });
    },
    async getMetadata(storageId) {
      assertId(storageId, "ctx.storage.getMetadata");
      const reply = await rpc("getMetadata", { storageId });
      if (reply === null || reply === undefined) return null;
      return reply as StorageFileMetadata;
    },
  };
}

/**
 * Build a read + write storage surface. Adds the three Convex-shape
 * mutation/action operations:
 *
 *   * `generateUploadUrl()` — server-to-server mint of a signed PUT URL.
 *   * `store(blob, opts?)`  — server-side upload streaming the bytes
 *     through the RPC channel. The main thread PUTs the bytes to the
 *     freshly-minted upload URL, calls confirm-upload, and returns the
 *     storage id.
 *   * `delete(id)`          — idempotent removal.
 */
export function createStorageWriter(rpc: StorageRpc): StorageWriter {
  const reader = createStorageReader(rpc);
  return {
    ...reader,
    async generateUploadUrl(declaration) {
      // The signature over the PUT binds both headers, so the client must
      // send exactly this type and this many bytes. Neither can be guessed
      // later, which is why they are required here.
      const contentType = declaration?.contentType ?? "";
      const size = declaration?.size ?? 0;
      if (typeof contentType !== "string" || contentType === "") {
        throw new Error("ctx.storage.generateUploadUrl: contentType is required");
      }
      if (typeof size !== "number" || !Number.isFinite(size) || size <= 0) {
        throw new Error("ctx.storage.generateUploadUrl: size is required and must be greater than zero");
      }
      const reply = await rpc("generateUploadUrl", { contentType, size });
      const minted = reply as Partial<MintedUpload> | null;
      if (typeof minted !== "object" || minted === null || typeof minted.url !== "string") {
        throw new Error("ctx.storage.generateUploadUrl: unexpected RPC reply shape");
      }
      if (typeof minted.storageId !== "string" || minted.storageId === "") {
        throw new Error("ctx.storage.generateUploadUrl: reply carried no storageId");
      }
      if (typeof minted.uploadId !== "string" || minted.uploadId === "") {
        throw new Error("ctx.storage.generateUploadUrl: reply carried no uploadId");
      }
      return { url: minted.url, storageId: minted.storageId, uploadId: minted.uploadId };
    },
    async completeUpload(completion) {
      // A direct upload is bytes in a staging area until something accepts
      // them. The browser cannot reach the internal route, so a function
      // does it — and after the grace an unconfirmed upload is collected.
      const storageId = completion?.storageId ?? "";
      const uploadId = completion?.uploadId ?? "";
      if (typeof storageId !== "string" || storageId === "") {
        throw new Error("ctx.storage.completeUpload: storageId is required");
      }
      if (typeof uploadId !== "string" || uploadId === "") {
        throw new Error("ctx.storage.completeUpload: uploadId is required");
      }
      const reply = await rpc("completeUpload", { storageId, uploadId });
      if (typeof reply !== "string" || reply.length === 0) {
        throw new Error("ctx.storage.completeUpload: RPC did not return a storage id");
      }
      return reply;
    },
    async store(blob, opts) {
      if (!blob || typeof (blob as Blob).arrayBuffer !== "function") {
        throw new Error("ctx.storage.store: blob argument is required");
      }
      const buf = await (blob as Blob).arrayBuffer();
      const bytes = new Uint8Array(buf);
      const reply = await rpc("store", {
        bytes,
        contentType: (blob as Blob).type || "application/octet-stream",
        sha256: opts?.sha256,
      });
      if (typeof reply !== "string" || reply.length === 0) {
        throw new Error("ctx.storage.store: RPC did not return a storage id");
      }
      return reply;
    },
    async delete(storageId) {
      assertId(storageId, "ctx.storage.delete");
      await rpc("delete", { storageId });
    },
  };
}
