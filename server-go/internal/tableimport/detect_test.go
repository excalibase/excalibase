package tableimport

import (
	"errors"
	"testing"
)

func TestDetectFormat_ByContentNotByName(t *testing.T) {
	cases := []struct {
		head []byte
		want Format
		err  error
	}{
		{[]byte("name,age\nann,3\n"), FormatCSV, nil},
		{[]byte("\ufeffa\tb\n"), FormatCSV, nil},
		{[]byte("PK\x03\x04rest-of-zip"), FormatXLSX, nil},
		{[]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1legacy"), "", ErrUnsupportedFormat},
		{[]byte("\x7fELF\x02\x01\x01\x00\x00"), "", ErrUnsupportedFormat},
		{[]byte("%PDF-1.7\n\x00\x01"), "", ErrUnsupportedFormat},
		{[]byte{}, "", ErrEmptyFile},
	}
	for _, c := range cases {
		got, err := DetectFormat(c.head)
		if !errors.Is(err, c.err) || got != c.want {
			t.Errorf("DetectFormat(%q) = %q, %v; want %q, %v", c.head, got, err, c.want, c.err)
		}
	}
}

// A multi-byte rune cut by the sniff window is not a binary file.
func TestDetectFormat_ACutRuneAtTheEndIsText(t *testing.T) {
	head := append([]byte("name\ncafé "), "é"[0])
	if got, err := DetectFormat(head); err != nil || got != FormatCSV {
		t.Fatalf("got %q, %v", got, err)
	}
}
