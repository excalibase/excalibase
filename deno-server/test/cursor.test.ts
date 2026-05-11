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
