package postgres

import "testing"

func TestPostStepSamplesAreCappedAtATenthOfThePool(t *testing.T) {
	for maxOpen, want := range map[int]int{0: 2, 1: 1, 5: 1, 20: 2, 100: 10} {
		if got := postStepCap(maxOpen); got != want {
			t.Errorf("pool of %d: %d samples may wait, want %d", maxOpen, got, want)
		}
	}
}
