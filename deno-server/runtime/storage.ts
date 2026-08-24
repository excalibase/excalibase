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
 *   generateUploadUrl → { url: string; storageId: string }
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

export interface StorageWriter extends StorageReader {
  generateUploadUrl(): Promise<string>;
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
    async generateUploadUrl() {
      const reply = await rpc("generateUploadUrl", {});
      if (
        typeof reply !== "object" ||
        reply === null ||
        typeof (reply as { url?: unknown }).url !== "string"
      ) {
        throw new Error("ctx.storage.generateUploadUrl: unexpected RPC reply shape");
      }
      return (reply as { url: string }).url;
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
