package tableimport

import (
	"bytes"
	"unicode/utf8"
)

// Format is what the file's content says it is.
type Format string

const (
	FormatCSV  Format = "csv"
	FormatXLSX Format = "xlsx"
)

// SniffBytes is how much of a file DetectFormat looks at.
const SniffBytes = 8 << 10

var zipMagic = []byte("PK\x03\x04")

// DetectFormat classifies a file by its first bytes; the name and the type
// the client declared are never consulted.
func DetectFormat(head []byte) (Format, error) {
	if len(head) == 0 {
		return "", ErrEmptyFile
	}
	if bytes.HasPrefix(head, zipMagic) {
		return FormatXLSX, nil
	}
	if bytes.IndexByte(head, 0) >= 0 || !validUTF8Prefix(head) {
		return "", ErrUnsupportedFormat
	}
	return FormatCSV, nil
}

// validUTF8Prefix tolerates one rune cut off by the sniff window.
func validUTF8Prefix(head []byte) bool {
	if utf8.Valid(head) {
		return true
	}
	for cut := 1; cut < utf8.UTFMax && cut < len(head); cut++ {
		if utf8.Valid(head[:len(head)-cut]) && !utf8.FullRune(head[len(head)-cut:]) {
			return true
		}
	}
	return false
}
