// CursorCodec — opaque base64url cursor for keyset pagination.
//
// Phase 5b uses the Convex-shape system fields (`_id` + `_creation_time`)
// for the keyset. Payload is `creationTime|id` where `creationTime` is the
// millisecond epoch float persisted in `_creation_time` and `id` is the
// 30-char base32 `_id`. The encoded form is base64url without padding —
// safe to ship as a URL query param.
//
// The pre-5b codec used an ISO-8601 instant + UUID derived from
// `created_at`/`id`. Cursors minted by that codec are no longer valid;
// callers must re-paginate from the start of a collection. There is no
// shim — Phase 5b is a hard break.

export interface CursorPayload {
  /** Millisecond epoch (Convex `_creationTime`). Stored as a float. */
  readonly creationTime: number;
  /** 30-char base32 id (`_id`). */
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
 * Encode a cursor for keyset pagination. Output is base64url without
 * padding. Payload format: `<creationTime>|<id>` where `creationTime` is
 * serialised as its plain JS number string (e.g. `1715472000123`) and `id`
 * is the 30-char `_id` string verbatim.
 */
export function encodeCursor(payload: CursorPayload): string {
  if (
    typeof payload.creationTime !== "number" ||
    !Number.isFinite(payload.creationTime) ||
    !payload.id
  ) {
    throw new Error("cursor payload requires a finite creationTime and a non-empty id");
  }
  const joined = `${payload.creationTime}|${payload.id}`;
  return toBase64Url(btoa(joined));
}

/**
 * Decode a cursor token. Throws on empty/blank/malformed input with the
 * same error vocabulary as the encoder (non-empty / malformed / not a
 * finite number).
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
  const ctRaw = decoded.substring(0, sep);
  const id = decoded.substring(sep + 1);
  const creationTime = Number(ctRaw);
  if (!Number.isFinite(creationTime)) {
    throw new Error("cursor creationTime not a finite number");
  }
  return { creationTime, id };
}
