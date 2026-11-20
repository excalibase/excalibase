package postgres

import "testing"

func TestLifecycleLockConns_IsAShareOfTheMainPool(t *testing.T) {
	for main, want := range map[int]int{20: 10, 40: 20, 7: 3, 2: 2, 1: 2} {
		if got := lifecycleLockConns(main); got != want {
			t.Errorf("main pool %d -> %d lock connections, want %d", main, got, want)
		}
	}
}
