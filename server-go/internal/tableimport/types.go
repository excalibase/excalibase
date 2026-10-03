package tableimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ColumnType is one of the column types an import may create. It is spliced
// into DDL, so only these exact values are ever accepted.
type ColumnType string

const (
	TypeText        ColumnType = "text"
	TypeInteger     ColumnType = "integer"
	TypeBigint      ColumnType = "bigint"
	TypeNumeric     ColumnType = "numeric"
	TypeBoolean     ColumnType = "boolean"
	TypeDate        ColumnType = "date"
	TypeTimestamptz ColumnType = "timestamptz"
	TypeUUID        ColumnType = "uuid"
	TypeJSONB       ColumnType = "jsonb"
)

var knownTypes = map[ColumnType]bool{
	TypeText: true, TypeInteger: true, TypeBigint: true, TypeNumeric: true, TypeBoolean: true,
	TypeDate: true, TypeTimestamptz: true, TypeUUID: true, TypeJSONB: true,
}

// ParseColumnType accepts only a known type, exactly as spelled.
func ParseColumnType(raw string) (ColumnType, error) {
	typ := ColumnType(raw)
	if !knownTypes[typ] {
		return "", fmt.Errorf("unknown column type %q", raw)
	}
	return typ, nil
}

var (
	integerPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	numericPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$|^-?0?\.[0-9]+$`)
	uuidPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	isoDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

var inferredBooleans = map[string]bool{"true": true, "false": true, "t": true, "f": true, "yes": true, "no": true}

// Postgres' own boolean spellings that CheckValue lets through.
var acceptedBooleans = map[string]bool{
	"true": true, "false": true, "t": true, "f": true, "yes": true, "no": true, "y": true, "n": true,
	"on": true, "off": true, "1": true, "0": true,
}

var timestampLayouts = []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04:05Z07:00"}

// inferenceOrder is narrowest first; the first type every sample fits wins.
var inferenceOrder = []ColumnType{TypeBoolean, TypeInteger, TypeBigint, TypeNumeric, TypeUUID, TypeDate, TypeTimestamptz, TypeJSONB}

// InferTypes guesses a type per column from sample rows. A column with no
// value, or one no single type fits, is text.
func InferTypes(rows [][]string, width int, nullTokens []string) []ColumnType {
	nulls := tokenSet(nullTokens)
	types := make([]ColumnType, width)
	for col := 0; col < width; col++ {
		types[col] = inferColumn(rows, col, nulls)
	}
	return types
}

func inferColumn(rows [][]string, col int, nulls map[string]bool) ColumnType {
	candidates := append([]ColumnType(nil), inferenceOrder...)
	seen := false
	for _, row := range rows {
		if col >= len(row) || nulls[row[col]] {
			continue
		}
		seen = true
		candidates = keepFitting(candidates, row[col])
		if len(candidates) == 0 {
			return TypeText
		}
	}
	if !seen {
		return TypeText
	}
	return candidates[0]
}

func keepFitting(candidates []ColumnType, value string) []ColumnType {
	kept := candidates[:0]
	for _, typ := range candidates {
		if infers(typ, value) {
			kept = append(kept, typ)
		}
	}
	return kept
}

// infers is stricter than CheckValue: 0/1 are numbers, dates must be ISO.
func infers(typ ColumnType, value string) bool {
	switch typ {
	case TypeBoolean:
		return inferredBooleans[strings.ToLower(value)]
	case TypeDate:
		if !isoDatePattern.MatchString(value) {
			return false
		}
		_, err := time.Parse("2006-01-02", value)
		return err == nil
	case TypeTimestamptz:
		return parsesTimestamp(value)
	case TypeJSONB:
		trimmed := strings.TrimSpace(value)
		return (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(value))
	default:
		return CheckValue(typ, value) == nil
	}
}

func parsesTimestamp(value string) bool {
	for _, layout := range timestampLayouts {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

var errNotInteger = errors.New("is not a whole number in range")

// CheckValue reports whether value fits typ before it is sent to Postgres.
// Dates and timestamps have many valid spellings; Postgres judges those.
func CheckValue(typ ColumnType, value string) error {
	switch typ {
	case TypeInteger:
		return checkInteger(value, math.MinInt32, math.MaxInt32)
	case TypeBigint:
		return checkInteger(value, math.MinInt64, math.MaxInt64)
	case TypeNumeric:
		if !numericPattern.MatchString(value) {
			return errors.New("is not a number (use digits and an optional decimal point)")
		}
	case TypeBoolean:
		if !acceptedBooleans[strings.ToLower(value)] {
			return errors.New("is not true/false, yes/no, on/off or 1/0")
		}
	case TypeUUID:
		if !uuidPattern.MatchString(value) {
			return errors.New("is not a UUID")
		}
	case TypeJSONB:
		if !json.Valid([]byte(value)) {
			return errors.New("is not valid JSON")
		}
	}
	return nil
}

func checkInteger(value string, lo, hi int64) error {
	if !integerPattern.MatchString(value) {
		return errNotInteger
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < lo || n > hi {
		return errNotInteger
	}
	return nil
}

func tokenSet(tokens []string) map[string]bool {
	set := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		set[t] = true
	}
	return set
}
