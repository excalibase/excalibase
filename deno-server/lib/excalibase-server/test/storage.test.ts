// ctx.storage type surface.
//
// The library only declares the contract:
//   * QueryCtx.storage   : StorageReader (read-only)
//   * MutationCtx.storage: StorageWriter (read + write + upload-url + store + delete)
//   * ActionCtx.storage  : StorageWriter
//
// Runtime supplies the concrete implementation; here we only assert the
// type surface and the minimal runtime structural pieces.

import type {
  ActionCtx,
  MutationCtx,
  QueryCtx,
  StorageFileMetadata,
  StorageReader,
  StorageWriter,
} from "../src";

// Tests run under jest's default lib set (ES2020 — no DOM Blob global). The
// real runtime ships Blob via Deno; in the library we only depend on the
// structural shape (`size` + `type`). Define a local stand-in so the type
// references resolve without pulling in lib.dom.
type BlobLike = { readonly size: number; readonly type: string };

// Local alias mirroring the runtime's storage-id brand. Inlined because
// the lib no longer exports a generic `Id<TableName>` brand.
type StorageId = string & { readonly __brand: "_storage" };

const asStorageId = (s: string): StorageId => s as StorageId;

describe("ctx.storage", () => {
  it("StorageFileMetadata carries required fields", () => {
    const md: StorageFileMetadata = {
      storageId: "kg2_abc",
      sha256: "deadbeef",
      size: 1024,
      contentType: "image/png",
    };
    expect(md.storageId).toBe("kg2_abc");
    expect(md.sha256).toBe("deadbeef");
    expect(md.size).toBe(1024);
    expect(md.contentType).toBe("image/png");
  });

  it("StorageFileMetadata.contentType is optional", () => {
    const md: StorageFileMetadata = {
      storageId: "kg2_xyz",
      sha256: "cafef00d",
      size: 0,
    };
    expect(md.contentType).toBeUndefined();
  });

  it("StorageReader exposes getUrl / get / getMetadata returning nullable promises", async () => {
    const reader: StorageReader = {
      getUrl: async (_id) => "https://r2.example/signed",
      get: async (_id) => null,
      getMetadata: async (_id) => null,
    };
    const id = asStorageId("kg2_abc");
    expect(await reader.getUrl(id)).toBe("https://r2.example/signed");
    expect(await reader.get(id)).toBeNull();
    expect(await reader.getMetadata(id)).toBeNull();
  });

  it("StorageWriter extends StorageReader with generateUploadUrl / store / delete", async () => {
    const writer: StorageWriter = {
      getUrl: async () => null,
      get: async () => null,
      getMetadata: async () => null,
      generateUploadUrl: async () => "https://r2.example/upload?sig=x",
      store: async (_blob, _opts) => asStorageId("kg2_new"),
      delete: async () => undefined,
    };
    const url = await writer.generateUploadUrl();
    expect(url).toBe("https://r2.example/upload?sig=x");
    const blob: BlobLike = { size: 4, type: "text/plain" };
    const newId = await writer.store(blob as never);
    expect(newId).toBe("kg2_new");
    await expect(writer.delete(newId)).resolves.toBeUndefined();
  });

  it("StorageWriter.store accepts optional sha256 opts", async () => {
    const calls: Array<{ sha?: string }> = [];
    const writer: StorageWriter = {
      getUrl: async () => null,
      get: async () => null,
      getMetadata: async () => null,
      generateUploadUrl: async () => "",
      store: async (_blob, opts) => {
        calls.push({ sha: opts?.sha256 });
        return asStorageId("kg2_x");
      },
      delete: async () => undefined,
    };
    const blob: BlobLike = { size: 1, type: "application/octet-stream" };
    await writer.store(blob as never);
    await writer.store(blob as never, { sha256: "abc" });
    expect(calls).toEqual([{ sha: undefined }, { sha: "abc" }]);
  });

  it("QueryCtx.storage is StorageReader (no write methods at compile time)", async () => {
    const qctx: QueryCtx = {
      db: null,
      auth: {
        claims: null,
        getUserIdentity: async () => null,
      },
      runQuery: async () => null as never,
      storage: {
        getUrl: async () => null,
        get: async () => null,
        getMetadata: async () => null,
      },
    };

    const id = asStorageId("kg2_q");
    const md = await qctx.storage.getMetadata(id);
    expect(md).toBeNull();

    // @ts-expect-error QueryCtx.storage is read-only — no generateUploadUrl
    qctx.storage.generateUploadUrl;
    // @ts-expect-error QueryCtx.storage is read-only — no store
    qctx.storage.store;
    // @ts-expect-error QueryCtx.storage is read-only — no delete
    qctx.storage.delete;
  });

  it("MutationCtx.storage is StorageWriter (read + write surface)", async () => {
    const mctx: MutationCtx = {
      db: null,
      auth: {
        claims: null,
        getUserIdentity: async () => null,
      },
      runQuery: async () => null as never,
      runMutation: async () => null as never,
      scheduler: {
        runAfter: async () => "sid" as never,
        runAt: async () => "sid" as never,
        cancel: async () => undefined,
      },
      storage: {
        getUrl: async () => "https://r2/get",
        get: async () => null,
        getMetadata: async () => null,
        generateUploadUrl: async () => "https://r2/up",
        store: async () => asStorageId("kg2_m"),
        delete: async () => undefined,
      },
    };

    expect(await mctx.storage.generateUploadUrl()).toBe("https://r2/up");
    const newId = await mctx.storage.store({ size: 1, type: "" } as never);
    expect(newId).toBe("kg2_m");
    await mctx.storage.delete(newId);
  });

  it("ActionCtx.storage is StorageWriter", async () => {
    const actx: ActionCtx = {
      db: null,
      auth: {
        claims: null,
        getUserIdentity: async () => null,
      },
      runQuery: async () => null as never,
      runMutation: async () => null as never,
      runAction: async () => null as never,
      scheduler: {
        runAfter: async () => "sid" as never,
        runAt: async () => "sid" as never,
        cancel: async () => undefined,
      },
      storage: {
        getUrl: async () => null,
        get: async () => null,
        getMetadata: async () => null,
        generateUploadUrl: async () => "https://r2/up-a",
        store: async () => asStorageId("kg2_a"),
        delete: async () => undefined,
      },
    };

    expect(await actx.storage.generateUploadUrl()).toBe("https://r2/up-a");
    const id = await actx.storage.store({ size: 0, type: "" } as never);
    expect(id).toBe("kg2_a");
  });
});
