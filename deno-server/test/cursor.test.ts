// CursorCodec round-trip tests.
//
// Phase 5b: cursors encode `creationTime|id` where creationTime is a
// millisecond epoch float (Convex `_creationTime`) and id is the 30-char
// base32 `_id`. Pre-5b's Java parity with `CursorCodec.encode(Instant,
// String)` is no longer relevant — the Java NoSQL service uses different
// keyset columns than the deno runtime now does.

import { assertEquals, assertThrows } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { decodeCursor, encodeCursor } from "../runtime/cursor.ts";

Deno.test("encode → decode round-trip", () => {
  const creationTime = 1715472000123;
  const id = "abc0123456789defghijklmnopqrst";
  const token = encodeCursor({ creationTime, id });
  const decoded = decodeCursor(token);
  assertEquals(decoded.creationTime, creationTime);
  assertEquals(decoded.id, id);
});

Deno.test("encode is base64url without padding", () => {
  const token = encodeCursor({
    creationTime: 1715472000123,
    id: "abc",
  });
  // base64url alphabet uses '-' and '_' instead of '+' and '/'.
  // We assert no '=' padding and no standard b64 chars.
  if (token.includes("=")) throw new Error("padding leaked: " + token);
  if (token.includes("+") || token.includes("/")) {
    throw new Error("standard b64 chars leaked: " + token);
  }
});

Deno.test("encode requires finite creationTime and non-empty id", () => {
  assertThrows(() => encodeCursor({ creationTime: NaN, id: "abc" }), Error);
  assertThrows(() => encodeCursor({ creationTime: 1, id: "" }), Error);
  assertThrows(() => encodeCursor({ creationTime: Infinity, id: "abc" }), Error);
});

Deno.test("decode rejects empty/null/blank input", () => {
  assertThrows(() => decodeCursor(""), Error, "non-empty");
  assertThrows(() => decodeCursor("   "), Error, "non-empty");
});

Deno.test("decode rejects malformed base64", () => {
  assertThrows(() => decodeCursor("!!!not-base64!!!"), Error);
});

Deno.test("decode rejects payload without '|' separator", () => {
  const token = btoa("no-separator-here")
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  assertThrows(() => decodeCursor(token), Error, "malformed");
});

Deno.test("decode rejects payload whose creationTime is not a finite number", () => {
  const payload = "not-a-number|abc";
  const token = btoa(payload)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  assertThrows(() => decodeCursor(token), Error, "finite");
});

// Phase 14 — snapshotTs watermark.
//
// The cursor blob is extended to a 3-field payload
// `creationTime|id|snapshotTs` so subsequent paginate() calls can exclude
// rows inserted after pagination started. Backwards-compat: cursors minted
// pre-14 use the 2-field shape `creationTime|id`; decode falls back to
// snapshotTs=undefined and the runtime treats that as "no snapshot yet,
// capture one on the next call".

Deno.test("encode → decode round-trip preserves snapshotTs when provided", () => {
  const creationTime = 1715472000123;
  const id = "abc0123456789defghijklmnopqrst";
  const snapshotTs = 1715472999999;
  const token = encodeCursor({ creationTime, id, snapshotTs });
  const decoded = decodeCursor(token);
  assertEquals(decoded.creationTime, creationTime);
  assertEquals(decoded.id, id);
  assertEquals(decoded.snapshotTs, snapshotTs);
});

Deno.test("encode → decode round-trip omits snapshotTs when not provided", () => {
  const creationTime = 1715472000123;
  const id = "abc0123456789defghijklmnopqrst";
  const token = encodeCursor({ creationTime, id });
  const decoded = decodeCursor(token);
  assertEquals(decoded.creationTime, creationTime);
  assertEquals(decoded.id, id);
  assertEquals(decoded.snapshotTs, undefined);
});

Deno.test("decode tolerates pre-Phase-14 2-field payloads (back-compat)", () => {
  // Hand-craft a 2-field token like the pre-14 encoder produced.
  const legacy = "1715472000123|abc0123456789defghijklmnopqrst";
  const token = btoa(legacy)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  const decoded = decodeCursor(token);
  assertEquals(decoded.creationTime, 1715472000123);
  assertEquals(decoded.id, "abc0123456789defghijklmnopqrst");
  assertEquals(decoded.snapshotTs, undefined);
});

Deno.test("encode rejects non-finite snapshotTs", () => {
  assertThrows(
    () => encodeCursor({ creationTime: 1, id: "abc", snapshotTs: NaN }),
    Error,
    "snapshotTs",
  );
  assertThrows(
    () => encodeCursor({ creationTime: 1, id: "abc", snapshotTs: Infinity }),
    Error,
    "snapshotTs",
  );
});

Deno.test("decode rejects payload whose snapshotTs is not a finite number", () => {
  const payload = "1715472000123|abc|not-a-number";
  const token = btoa(payload)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  assertThrows(() => decodeCursor(token), Error, "snapshotTs");
});
