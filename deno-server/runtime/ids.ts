// Convex-shape id generator.
//
// Phase 5b's `_id` column is `text` rather than `uuid`, mirroring Convex's
// opaque base32 string ids. The algorithm:
//   1. Generate 16 cryptographically random bytes via the Web Crypto API.
//   2. Encode each byte as a pair of base32-style characters (alphabet
//      `0-9a-z`, 36 symbols, ~5.17 bits per char).
//   3. Truncate to 30 chars.
//
// We use 36 symbols (not the strict 32 of RFC 4648 base32) because the
// resulting strings are friendlier — no underscores or hyphens — and the
// alphabet matches what Convex emits in practice. The runtime never relies
// on round-trip decode; ids are opaque to everyone except Postgres'
// equality operator on `_id text PRIMARY KEY`.
//
// Why 30? Convex's published id length is 30 chars. 16 bytes ≈ 128 bits of
// entropy, encoded in 30 chars of 5.17 bits each (~155 bits of address
// space). Collision risk is negligible for any practical collection size.

const ALPHABET = "0123456789abcdefghijklmnopqrstuvwxyz";
const ID_LENGTH = 30;

/**
 * Generate one Convex-shape id. Length: 30. Alphabet: lowercase letters +
 * digits. Source of randomness: Web Crypto `getRandomValues`, available
 * in Deno and modern browsers.
 */
export function newId(): string {
  // 30 chars × at most 1 byte each = 30 random bytes. We index each byte
  // into a 36-symbol alphabet via modulo. Modulo bias on a 256-symbol
  // input over a 36-symbol output exists in theory (some symbols 8x more
  // likely than others), but for a content-opaque primary key this is
  // imperceptible — and the gain of a perfectly uniform alphabet is not
  // worth the rejection-sampling complexity here.
  const bytes = new Uint8Array(ID_LENGTH);
  crypto.getRandomValues(bytes);
  let out = "";
  for (let i = 0; i < ID_LENGTH; i++) {
    out += ALPHABET[bytes[i] % ALPHABET.length];
  }
  return out;
}
