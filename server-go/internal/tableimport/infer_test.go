package tableimport

import "testing"

func column(values ...string) [][]string {
	rows := make([][]string, len(values))
	for i, v := range values {
		rows[i] = []string{v}
	}
	return rows
}

func TestInferType(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   ColumnType
	}{
		{"integers", []string{"1", "-42", "", "7"}, TypeInteger},
		{"bigints", []string{"1", "9223372036854775807"}, TypeBigint},
		{"leading zeros stay text (zip codes)", []string{"00123", "12345"}, TypeText},
		{"decimals", []string{"1.5", "2", "-0.25"}, TypeNumeric},
		{"booleans", []string{"true", "FALSE", "yes", "No"}, TypeBoolean},
		{"0/1 is a number, not a boolean", []string{"0", "1"}, TypeInteger},
		{"iso dates", []string{"2026-09-30", "2020-02-29"}, TypeDate},
		{"invalid date is text", []string{"2026-02-30"}, TypeText},
		{"timestamps", []string{"2026-09-30T10:00:00Z", "2026-09-30 10:00:00"}, TypeTimestamptz},
		{"uuids", []string{"3f0a6c1e-1d5b-4c0e-9d4a-2b8f0e7a1c55"}, TypeUUID},
		{"json objects", []string{`{"a":1}`, `[1,2]`}, TypeJSONB},
		{"json-looking but invalid is text", []string{`{"a":`}, TypeText},
		{"mixed falls back to text", []string{"1", "abc"}, TypeText},
		{"all empty is text", []string{"", ""}, TypeText},
		{"formula text stays text", []string{"=cmd|' /C calc'!A0", "=1+1"}, TypeText},
		{"number overflowing numeric grammar is text", []string{"1e5"}, TypeText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := InferTypes(column(c.values...), 1, []string{""})
			if got[0] != c.want {
				t.Fatalf("InferTypes(%v) = %s, want %s", c.values, got[0], c.want)
			}
		})
	}
}

func TestInferTypes_NullTokensAreIgnored(t *testing.T) {
	got := InferTypes(column("1", "NULL", "N/A", "3"), 1, []string{"", "NULL", "N/A"})
	if got[0] != TypeInteger {
		t.Fatalf("got %s", got[0])
	}
}

func TestInferTypes_RaggedRowsDoNotPanic(t *testing.T) {
	got := InferTypes([][]string{{"1"}, {"2", "x"}, {}}, 2, []string{""})
	if got[0] != TypeInteger || got[1] != TypeText {
		t.Fatalf("got %v", got)
	}
}

func TestCheckValue(t *testing.T) {
	good := map[ColumnType][]string{
		TypeInteger:     {"0", "-2147483648", "2147483647"},
		TypeBigint:      {"-9223372036854775808"},
		TypeNumeric:     {"3.14", "-1", "10"},
		TypeBoolean:     {"t", "False", "YES", "off", "1", "0"},
		TypeUUID:        {"3F0A6C1E-1D5B-4C0E-9D4A-2B8F0E7A1C55"},
		TypeJSONB:       {`{"k":[1,true,null]}`, `"s"`, `3`},
		TypeText:        {"anything at all", "=HYPERLINK(\"http://x\")"},
		TypeDate:        {"2026-09-30", "9/30/2026"},
		TypeTimestamptz: {"2026-09-30T10:00:00+07:00"},
	}
	for typ, values := range good {
		for _, v := range values {
			if err := CheckValue(typ, v); err != nil {
				t.Errorf("CheckValue(%s, %q) = %v", typ, v, err)
			}
		}
	}
	bad := map[ColumnType][]string{
		TypeInteger: {"2147483648", "1.0", "abc", " 1"},
		TypeBigint:  {"9223372036854775808"},
		TypeNumeric: {"1,000", "NaNx", "1e"},
		TypeBoolean: {"maybe"},
		TypeUUID:    {"not-a-uuid"},
		TypeJSONB:   {`{"a":`},
	}
	for typ, values := range bad {
		for _, v := range values {
			if CheckValue(typ, v) == nil {
				t.Errorf("CheckValue(%s, %q) accepted", typ, v)
			}
		}
	}
}

func TestParseColumnType_RefusesUnknownTypes(t *testing.T) {
	for _, bad := range []string{"", "varchar(10); DROP TABLE x", "money", "TEXT "} {
		if _, err := ParseColumnType(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if typ, err := ParseColumnType("timestamptz"); err != nil || typ != TypeTimestamptz {
		t.Fatalf("timestamptz: %v %v", typ, err)
	}
}
