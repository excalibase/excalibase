// The upload protocol stages bytes before they become an object: the
// platform mints an upload id, the bytes are PUT to a staging key, and a
// confirmation naming that id moves them onto the object's key. Nothing is
// recorded — and after the grace nothing is kept — without that confirmation.
//
// These pin the worker-side facade against that protocol. The HTTP bodies the
// main thread sends are pinned in storage_integration.test.ts.

import { assertEquals, assertRejects } from "https://deno.land/std@0.224.0/assert/mod.ts";
import {
  createStorageWriter,
  type StorageRpc,
} from "../runtime/storage.ts";

interface RecordedCall {
  op: string;
  payload: unknown;
}

function recordingRpc(replies: ReadonlyArray<unknown>): {
  rpc: StorageRpc;
  calls: RecordedCall[];
} {
  const calls: RecordedCall[] = [];
  let next = 0;
  const rpc: StorageRpc = (op, payload) => {
    calls.push({ op, payload });
    return Promise.resolve(replies[next++]);
  };
  return { rpc, calls };
}

// The signed PUT binds the length and the type, so the caller has to say what
// it is about to upload before a URL can be minted for it.
Deno.test("generateUploadUrl declares the upload's type and size", async () => {
  const { rpc, calls } = recordingRpc([
    { url: "https://r2.test/.staging/upl_1?sig=y", storageId: "kg2_new", uploadId: "upl_1" },
  ]);
  const writer = createStorageWriter(rpc);

  const minted = await writer.generateUploadUrl({ contentType: "image/png", size: 1234 });

  assertEquals(calls[0].op, "generateUploadUrl");
  assertEquals(calls[0].payload, { contentType: "image/png", size: 1234 });
  assertEquals(minted.url, "https://r2.test/.staging/upl_1?sig=y");
  assertEquals(minted.storageId, "kg2_new");
  // The upload id is what the later confirmation names; the URL alone no
  // longer identifies the bytes.
  assertEquals(minted.uploadId, "upl_1");
});

Deno.test("generateUploadUrl refuses a request with no type or size", async () => {
  const { rpc } = recordingRpc([{}]);
  const writer = createStorageWriter(rpc);
  await assertRejects(
    () => writer.generateUploadUrl({ contentType: "", size: 10 }),
    Error,
    "contentType",
  );
  await assertRejects(
    () => writer.generateUploadUrl({ contentType: "image/png", size: 0 }),
    Error,
    "size",
  );
});

Deno.test("generateUploadUrl rejects a reply that does not name the upload", async () => {
  const { rpc } = recordingRpc([{ url: "https://r2.test/x", storageId: "kg2_new" }]);
  const writer = createStorageWriter(rpc);
  await assertRejects(
    () => writer.generateUploadUrl({ contentType: "image/png", size: 1 }),
    Error,
    "uploadId",
  );
});

// A direct upload becomes an object only when something confirms it. The
// browser cannot reach the internal route, so a function does it.
Deno.test("completeUpload confirms a staged direct upload", async () => {
  const { rpc, calls } = recordingRpc(["kg2_new"]);
  const writer = createStorageWriter(rpc);

  const id = await writer.completeUpload({ storageId: "kg2_new", uploadId: "upl_1" });

  assertEquals(id, "kg2_new");
  assertEquals(calls[0].op, "completeUpload");
  assertEquals(calls[0].payload, { storageId: "kg2_new", uploadId: "upl_1" });
});

Deno.test("completeUpload requires both the storage id and the upload id", async () => {
  const { rpc } = recordingRpc(["kg2_new", "kg2_new"]);
  const writer = createStorageWriter(rpc);
  await assertRejects(
    () => writer.completeUpload({ storageId: "", uploadId: "upl_1" }),
    Error,
    "storageId",
  );
  await assertRejects(
    () => writer.completeUpload({ storageId: "kg2_new", uploadId: "" }),
    Error,
    "uploadId",
  );
});
