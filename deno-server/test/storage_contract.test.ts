// The runtime and provisioning are one contract compiled by two toolchains:
// nothing in the Go build fails when a field is renamed here, and nothing
// here fails when it is renamed there. These pin the JSON field names the
// runtime puts on the wire, from this side.
//
// Provisioning's half is pinned in
// server-go/internal/handler/storage_internal_contract_test.go.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { createStorageWriter, type StorageRpc } from "../runtime/storage.ts";

const UPDATE_GO_TOO =
  "provisioning speaks this shape — update server-go/internal/handler/storage.go " +
  "and its contract test";

function capture(replies: ReadonlyArray<unknown>) {
  const calls: Array<{ op: string; payload: Record<string, unknown> }> = [];
  let next = 0;
  const rpc: StorageRpc = (op, payload) => {
    calls.push({ op, payload: payload as Record<string, unknown> });
    return Promise.resolve(replies[next++]);
  };
  return { rpc, calls };
}

Deno.test("the upload-url RPC declares exactly contentType and size", async () => {
  const { rpc, calls } = capture([
    { url: "u", storageId: "kg2_1", uploadId: "upl_1" },
  ]);
  await createStorageWriter(rpc).generateUploadUrl({ contentType: "image/png", size: 7 });
  assertEquals(Object.keys(calls[0].payload).sort(), ["contentType", "size"], UPDATE_GO_TOO);
});

Deno.test("the confirm RPC names exactly the storage id and the upload id", async () => {
  const { rpc, calls } = capture(["kg2_1"]);
  await createStorageWriter(rpc).completeUpload({ storageId: "kg2_1", uploadId: "upl_1" });
  assertEquals(Object.keys(calls[0].payload).sort(), ["storageId", "uploadId"], UPDATE_GO_TOO);
});

Deno.test("the store RPC carries the bytes, their type, and an optional digest", async () => {
  const { rpc, calls } = capture(["kg2_1"]);
  const blob = new Blob(["hello"], { type: "text/plain" });
  await createStorageWriter(rpc).store(blob, { sha256: "ff" });
  assertEquals(Object.keys(calls[0].payload).sort(), ["bytes", "contentType", "sha256"], UPDATE_GO_TOO);
  // store declares the blob's own size and type, and the main thread sends
  // exactly those on the PUT — the signature covers both.
  const payload = calls[0].payload as { bytes: Uint8Array; contentType: string };
  assertEquals(payload.bytes.byteLength, blob.size);
  assertEquals(payload.contentType, blob.type);
});

Deno.test("the reader RPCs name only the storage id", async () => {
  const { rpc, calls } = capture([null, null, null]);
  const writer = createStorageWriter(rpc);
  await writer.getUrl("kg2_1");
  await writer.getMetadata("kg2_1");
  await writer.delete("kg2_1");
  for (const call of calls) {
    assertEquals(Object.keys(call.payload), ["storageId"], UPDATE_GO_TOO);
  }
});
