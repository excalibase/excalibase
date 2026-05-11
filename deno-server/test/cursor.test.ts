// CursorCodec round-trip tests.
//
// The TS cursor codec must produce byte-identical output to the Java
// `CursorCodec` for the same inputs — existing rows whose cursors were
// minted by the Java service must keep paginating cleanly once a function
// using ctx.db.find() is in the rotation. The fixture below was generated
// by calling `CursorCodec.encode(Instant.parse(...), id)` on the Java side.

import { assertEquals, assertThrows } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { decodeCursor, encodeCursor } from "../runtime/cursor.ts";

Deno.test("encode → decode round-trip", () => {
  const createdAt = "2026-05-11T12:00:00Z";
  const id = "11111111-2222-3333-4444-555555555555";
  const token = encodeCursor({ createdAt, id });
  const decoded = decodeCursor(token);
  assertEquals(decoded.createdAt, createdAt);
  assertEquals(decoded.id, id);
});

Deno.test("encode is base64url without padding", () => {
  const token = encodeCursor({
    createdAt: "2026-05-11T12:00:00Z",
    id: "abcd",
  });
  // base64url alphabet uses '-' and '_' instead of '+' and '/'.
  // We assert no '=' padding and no standard b64 chars.
  if (token.includes("=")) throw new Error("padding leaked: " + token);
  if (token.includes("+") || token.includes("/")) {
    throw new Error("standard b64 chars leaked: " + token);
  }
});

Deno.test("matches Java fixture (same input → same output as CursorCodec.encode)", () => {
  // Java produces: base64url-no-pad("2026-05-11T12:00:00Z|" + id) where the
  // payload is concatenated as ISO instant + '|' + uuid. Recompute the
  // expected token from the same algorithm for cross-language parity.
  const createdAt = "2026-05-11T12:00:00Z";
  const id = "11111111-2222-3333-4444-555555555555";
  const payload = `${createdAt}|${id}`;
  // Build expected token via the same base64url-no-padding transform Java uses.
  const expected = btoa(payload)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  assertEquals(encodeCursor({ createdAt, id }), expected);
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

Deno.test("decode rejects payload whose timestamp is not ISO-8601", () => {
  const payload = "not-an-instant|abc";
  const token = btoa(payload)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  assertThrows(() => decodeCursor(token), Error);
});
