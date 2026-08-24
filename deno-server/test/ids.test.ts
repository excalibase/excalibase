// Unit tests for the Convex-shape id generator.
//
// Phase 5b ids are 30-char lowercase base32 (alphabet: 0-9a-z minus the
// 4 ambiguity-prone letters convex drops? No — Convex keeps the full 36
// alphanumeric set; we use the full 0-9a-z lowercase set, take 30 chars
// from 16 random bytes via base32 encoding. The simpler approach we use:
// 16 random bytes → 32-char hex/base32 → first 30 chars. The properties
// the rest of the runtime relies on are:
//   * Output length is exactly 30.
//   * Each character is in [0-9a-z].
//   * Two consecutive calls produce different ids.
//
// The runtime imports the generator from `runtime/ids.ts`; the tests
// import it directly.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { newId } from "../runtime/ids.ts";

Deno.test("newId returns a 30-char string", () => {
  const id = newId();
  assertEquals(id.length, 30);
});

Deno.test("newId uses only lowercase letters and digits", () => {
  for (let i = 0; i < 100; i++) {
    const id = newId();
    if (!/^[a-z0-9]{30}$/.test(id)) {
      throw new Error(`id ${id} fails the 30-char [a-z0-9] regex`);
    }
  }
});

Deno.test("newId yields distinct values across calls", () => {
  const seen = new Set<string>();
  for (let i = 0; i < 1000; i++) {
    seen.add(newId());
  }
  // 30 chars over 36 alphabet → ~155 bits of entropy. 1000 draws should
  // never collide unless something is very wrong with the RNG.
  assertEquals(seen.size, 1000);
});
