package shamir

import "testing"

// The x coordinates come from crypto/rand (EXC-642): every value of [0, n)
// exactly once.
func TestCryptoPerm_IsAPermutation(t *testing.T) {
	perm, err := cryptoPerm(255)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[int]bool, len(perm))
	for _, v := range perm {
		if v < 0 || v >= 255 || seen[v] {
			t.Fatalf("not a permutation of [0, 255): %v", perm)
		}
		seen[v] = true
	}
	if len(seen) != 255 {
		t.Fatalf("got %d distinct values, want 255", len(seen))
	}
}
