// CursorCodec — opaque base64url cursor for keyset pagination.
//
// Ported verbatim from `CursorCodec.java` in excalibase-nosql so existing
// cursors minted by the Java service keep working when read by a v2
// function that paginates via `ctx.db.find()`. Payload is
// `createdAt|id` (ISO instant + UUID) encoded as base64url without padding.

export interface CursorPayload {
  readonly createdAt: string;
  readonly id: string;
}

function toBase64Url(stdB64: string): string {
  return stdB64
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

function fromBase64Url(urlB64: string): string {
  // Restore standard base64 charset for atob, then re-pad.
  let s = urlB64.replace(/-/g, "+").replace(/_/g, "/");
  const pad = s.length % 4;
  if (pad === 2) s += "==";
  else if (pad === 3) s += "=";
  else if (pad === 1) throw new Error("cursor is not valid base64-url");
  return s;
}

/**
 * Encode a cursor for keyset pagination. Output is base64url without padding
 * and byte-identical to `CursorCodec.encode(Instant, String)` on the Java side.
 */
export function encodeCursor(payload: CursorPayload): string {
  if (!payload.createdAt || !payload.id) {
    throw new Error("cursor payload requires both createdAt and id");
  }
  const joined = `${payload.createdAt}|${payload.id}`;
  return toBase64Url(btoa(joined));
}

const ISO_INSTANT_RE = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$/;

/**
 * Decode a cursor token. Throws on empty/blank/malformed input with the same
 * error vocabulary as the Java version (non-empty / malformed / not ISO-8601).
 */
export function decodeCursor(token: string): CursorPayload {
  if (!token || token.trim().length === 0) {
    throw new Error("cursor must be a non-empty string");
  }
  let decoded: string;
  try {
    decoded = atob(fromBase64Url(token));
  } catch (_e) {
    throw new Error("cursor is not valid base64-url");
  }
  const sep = decoded.indexOf("|");
  if (sep <= 0 || sep === decoded.length - 1) {
    throw new Error("cursor payload malformed");
  }
  const createdAt = decoded.substring(0, sep);
  const id = decoded.substring(sep + 1);
  if (!ISO_INSTANT_RE.test(createdAt)) {
    throw new Error("cursor createdAt not ISO-8601");
  }
  return { createdAt, id };
}
