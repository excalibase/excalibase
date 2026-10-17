package k8s

import "testing"

func TestCappedBufferKeepsTheFirstBytesAndSwallowsTheRest(t *testing.T) {
	buffer := &cappedBuffer{limit: 5}
	for _, chunk := range []string{"abc", "defgh", "ij"} {
		if n, err := buffer.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if buffer.String() != "abcde" {
		t.Fatalf("kept %q", buffer.String())
	}
}
