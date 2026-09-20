// Phase 10 — unit tests for ctx.storage RPC dispatch (no real R2/Postgres).
//
// The worker-side facade lives in runtime/storage.ts and posts RPC
// messages of shape `{type: "storage", op, payload, rpcId}` to the main
// thread. The main thread translates each op into a call against
// provisioning's `/internal/storage/{projectId}/...` routes, with the
// runtime-token header set, and posts back `{type: "storageResult", rpcId,
// result}`.
//
// These tests pin the facade behaviour in isolation: we instantiate the
// reader/writer directly and assert the messages it posts plus the value
// resolved when a fake main-thread reply is delivered.
//
// Integration with the actual provisioning HTTP route lives in
// storage_integration.test.ts.

import { assertEquals, assertRejects } from "https://deno.land/std@0.224.0/assert/mod.ts";
import {
  createStorageReader,
  createStorageWriter,
  type StorageRpc,
} from "../runtime/storage.ts";

// fakeRpc is the seam the facade calls into. It records the requests then
// resolves them with the next entry from `replies` (or rejects if `next`
// returns a rejection-shaped value).
interface RecordedCall {
  op: string;
  payload: unknown;
}
function makeFakeRpc(replies: ReadonlyArray<unknown>): {
  rpc: StorageRpc;
  calls: RecordedCall[];
} {
  const calls: RecordedCall[] = [];
  let next = 0;
  const rpc: StorageRpc = async (op: string, payload: unknown) => {
    calls.push({ op, payload });
    const reply = replies[next++];
    if (reply !== undefined && typeof reply === "object" && reply !== null
        && "__error" in (reply as Record<string, unknown>)) {
      throw new Error(String((reply as { __error: unknown }).__error));
    }
    return reply;
  };
  return { rpc, calls };
}

Deno.test("StorageReader.getUrl posts a getUrl RPC and returns the URL string", async () => {
  const { rpc, calls } = makeFakeRpc(["https://r2.test/signed-get?sig=x"]);
  const reader = createStorageReader(rpc);
  const url = await reader.getUrl("kg2_abc");
  assertEquals(url, "https://r2.test/signed-get?sig=x");
  assertEquals(calls.length, 1);
  assertEquals(calls[0].op, "getUrl");
  assertEquals(calls[0].payload, { storageId: "kg2_abc" });
});

Deno.test("StorageReader.getUrl returns null when the RPC reply is null", async () => {
  const { rpc } = makeFakeRpc([null]);
  const reader = createStorageReader(rpc);
  const url = await reader.getUrl("kg2_missing");
  assertEquals(url, null);
});

Deno.test("StorageReader.getMetadata returns the metadata object on success", async () => {
  const meta = {
    storageId: "kg2_abc",
    sha256: "deadbeef",
    size: 42,
    contentType: "image/png",
  };
  const { rpc, calls } = makeFakeRpc([meta]);
  const reader = createStorageReader(rpc);
  const out = await reader.getMetadata("kg2_abc");
  assertEquals(out, meta);
  assertEquals(calls[0].op, "getMetadata");
  assertEquals(calls[0].payload, { storageId: "kg2_abc" });
});

Deno.test("StorageReader.getMetadata returns null when the RPC reply is null", async () => {
  const { rpc } = makeFakeRpc([null]);
  const reader = createStorageReader(rpc);
  const out = await reader.getMetadata("kg2_missing");
  assertEquals(out, null);
});

Deno.test("StorageReader.get returns null when the RPC reply is null", async () => {
  const { rpc } = makeFakeRpc([null]);
  const reader = createStorageReader(rpc);
  const blob = await reader.get("kg2_missing");
  assertEquals(blob, null);
});

Deno.test("StorageReader.get hands back a Blob when bytes come over the RPC", async () => {
  // Main-thread reply shape: { bytes, contentType }. Worker wraps in Blob.
  const reply = {
    bytes: new Uint8Array([72, 105]), // "Hi"
    contentType: "text/plain",
  };
  const { rpc } = makeFakeRpc([reply]);
  const reader = createStorageReader(rpc);
  const blob = await reader.get("kg2_abc");
  if (blob === null) throw new Error("expected non-null blob");
  assertEquals(blob.type, "text/plain");
  const text = await blob.text();
  assertEquals(text, "Hi");
});

Deno.test("StorageWriter.generateUploadUrl resolves to the staged upload", async () => {
  const { rpc, calls } = makeFakeRpc([
    { url: "https://r2.test/upload?sig=y", storageId: "kg2_new", uploadId: "upl_1" },
  ]);
  const writer = createStorageWriter(rpc);
  const minted = await writer.generateUploadUrl({ contentType: "text/plain", size: 3 });
  assertEquals(minted.url, "https://r2.test/upload?sig=y");
  assertEquals(minted.uploadId, "upl_1");
  assertEquals(calls[0].op, "generateUploadUrl");
});

Deno.test("StorageWriter.store streams the blob via the RPC and returns storageId", async () => {
  // store posts the blob's bytes inside the RPC payload. The main thread
  // then PUTs to the signed URL it minted internally.
  const { rpc, calls } = makeFakeRpc(["kg2_stored"]);
  const writer = createStorageWriter(rpc);
  const blob = new Blob([new Uint8Array([1, 2, 3, 4])], { type: "application/octet-stream" });
  const id = await writer.store(blob, { sha256: "abc" });
  assertEquals(id, "kg2_stored");
  assertEquals(calls.length, 1);
  assertEquals(calls[0].op, "store");
  // Payload carries bytes + contentType + sha256.
  const payload = calls[0].payload as {
    bytes: Uint8Array;
    contentType: string;
    sha256?: string;
  };
  assertEquals(payload.bytes.length, 4);
  assertEquals(payload.bytes[0], 1);
  assertEquals(payload.bytes[3], 4);
  assertEquals(payload.contentType, "application/octet-stream");
  assertEquals(payload.sha256, "abc");
});

Deno.test("StorageWriter.store omits sha256 when not provided", async () => {
  const { rpc, calls } = makeFakeRpc(["kg2_stored"]);
  const writer = createStorageWriter(rpc);
  const blob = new Blob(["hello"], { type: "text/plain" });
  await writer.store(blob);
  const payload = calls[0].payload as { sha256?: string };
  assertEquals(payload.sha256, undefined);
});

Deno.test("StorageWriter.delete resolves to undefined on success", async () => {
  const { rpc, calls } = makeFakeRpc([null]);
  const writer = createStorageWriter(rpc);
  const out = await writer.delete("kg2_x");
  assertEquals(out, undefined);
  assertEquals(calls[0].op, "delete");
  assertEquals(calls[0].payload, { storageId: "kg2_x" });
});

Deno.test("StorageReader.getUrl rejects on a non-string id", async () => {
  const { rpc } = makeFakeRpc([]);
  const reader = createStorageReader(rpc);
  await assertRejects(
    () => reader.getUrl("" as never),
    Error,
    "storageId",
  );
});

Deno.test("StorageWriter.store rejects when blob is missing", async () => {
  const { rpc } = makeFakeRpc([]);
  const writer = createStorageWriter(rpc);
  await assertRejects(
    () => writer.store(undefined as unknown as Blob),
    Error,
    "blob",
  );
});

Deno.test("StorageWriter.delete rejects on empty id", async () => {
  const { rpc } = makeFakeRpc([]);
  const writer = createStorageWriter(rpc);
  await assertRejects(
    () => writer.delete("" as never),
    Error,
    "storageId",
  );
});

Deno.test("getUrl rejects when the RPC reply is not a string", async () => {
  const { rpc } = makeFakeRpc([{ wrong: "shape" }]);
  const reader = createStorageReader(rpc);
  await assertRejects(
    () => reader.getUrl("kg2_x"),
    Error,
    "unexpected RPC reply type",
  );
});

Deno.test("get rejects when the RPC reply lacks a 'bytes' field", async () => {
  const { rpc } = makeFakeRpc([{ size: 0 }]);
  const reader = createStorageReader(rpc);
  await assertRejects(
    () => reader.get("kg2_x"),
    Error,
    "unexpected RPC reply type",
  );
});

Deno.test("generateUploadUrl rejects on malformed reply", async () => {
  // A declaration is present; the reply is the problem.
  const { rpc } = makeFakeRpc([{ noUrl: "yikes" }]);
  const writer = createStorageWriter(rpc);
  await assertRejects(
    () => writer.generateUploadUrl({ contentType: "text/plain", size: 1 }),
    Error,
    "unexpected RPC reply shape",
  );
});

Deno.test("store rejects when RPC returns a non-string", async () => {
  const { rpc } = makeFakeRpc([{ foo: "bar" }]);
  const writer = createStorageWriter(rpc);
  const blob = new Blob(["x"], { type: "text/plain" });
  await assertRejects(
    () => writer.store(blob),
    Error,
    "did not return a storage id",
  );
});

Deno.test("RPC error propagates as a rejected promise", async () => {
  const { rpc } = makeFakeRpc([{ __error: "upstream 500" }]);
  const reader = createStorageReader(rpc);
  await assertRejects(
    () => reader.getUrl("kg2_x"),
    Error,
    "upstream 500",
  );
});
