// CursorCodec — opaque base64url cursor for keyset pagination.
//
// Phase 5b uses the Convex-shape system fields (`_id` + `_creation_time`)
// for the keyset. The pre-5b codec used an ISO-8601 instant + UUID derived
// from `created_at`/`id`. Cursors minted by that codec are no longer valid;
// callers must re-paginate from the start of a collection. There is no
// shim — Phase 5b is a hard break.
//
// Phase 14 extends the encoded blob with a `snapshotTs` watermark used by
// `.paginate()` for snapshot consistency under concurrent writes (see
// `runtime/db.ts` paginate terminal). Payload is now 3-field:
// `creationTime|id|snapshotTs`. The pre-14 2-field form
// (`creationTime|id`) is still accepted on decode — `snapshotTs` resolves
// to `undefined` and the caller treats that as "first call, capture a
// fresh snapshot now". The encoded form is base64url without padding, so
// it remains safe to ship as a URL query param.

export interface CursorPayload {
  /** Millisecond epoch (Convex `_creationTime`). Stored as a float. */
  readonly creationTime: number;
  /** 30-char base32 id (`_id`). */
  readonly id: string;
  /**
   * Phase 14 — snapshot watermark captured at the start of a
   * `.paginate()` chain. Subsequent pages add
   * `_creation_time <= snapshotTs` so rows inserted after the snapshot
   * are excluded. Optional: omitted on pre-14 cursors, in which case the
   * caller captures a fresh snapshot on the next paginate call.
   */
  readonly snapshotTs?: number;
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
 * padding. Payload format: `<creationTime>|<id>` (pre-14) or
 * `<creationTime>|<id>|<snapshotTs>` (Phase 14+) where each number is
 * serialised as its plain JS number string and `id` is the 30-char
 * `_id` string verbatim.
 */
export function encodeCursor(payload: CursorPayload): string {
  if (
    typeof payload.creationTime !== "number" ||
    !Number.isFinite(payload.creationTime) ||
    !payload.id
  ) {
    throw new Error("cursor payload requires a finite creationTime and a non-empty id");
  }
  if (payload.snapshotTs !== undefined) {
    if (typeof payload.snapshotTs !== "number" || !Number.isFinite(payload.snapshotTs)) {
      throw new Error("cursor payload snapshotTs must be a finite number");
    }
    const joined = `${payload.creationTime}|${payload.id}|${payload.snapshotTs}`;
    return toBase64Url(btoa(joined));
  }
  const joined = `${payload.creationTime}|${payload.id}`;
  return toBase64Url(btoa(joined));
}

/**
 * Decode a cursor token. Throws on empty/blank/malformed input with the
 * same error vocabulary as the encoder (non-empty / malformed / not a
 * finite number). Backwards-compat: 2-field payloads decode without a
 * `snapshotTs`; 3-field payloads return all three fields.
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
  const parts = decoded.split("|");
  if (parts.length < 2 || parts.length > 3) {
    throw new Error("cursor payload malformed");
  }
  const ctRaw = parts[0];
  const id = parts[1];
  if (ctRaw.length === 0 || id.length === 0) {
    throw new Error("cursor payload malformed");
  }
  const creationTime = Number(ctRaw);
  if (!Number.isFinite(creationTime)) {
    throw new Error("cursor creationTime not a finite number");
  }
  if (parts.length === 3) {
    const tsRaw = parts[2];
    if (tsRaw.length === 0) {
      throw new Error("cursor payload malformed");
    }
    const snapshotTs = Number(tsRaw);
    if (!Number.isFinite(snapshotTs)) {
      throw new Error("cursor snapshotTs not a finite number");
    }
    return { creationTime, id, snapshotTs };
  }
  return { creationTime, id };
}
