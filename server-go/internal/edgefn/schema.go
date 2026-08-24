package edgefn

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/dop251/goja"
)

// Schema mirrors @excalibase/server's SchemaJsonSchema TypeScript export.
// Stored as JSON on the Function record and consumed by ApplySchema.
type Schema struct {
	Tables map[string]TableSchema `json:"tables"`
}

// TableSchema is the per-table breakdown the migrator consumes. Fields mirror
// the JSON Schema produced by defineSchema.toJsonSchema().
type TableSchema struct {
	Validator     ValidatorSchemaObject `json:"validator"`
	Indexes       []IndexSpec           `json:"indexes"`
	SearchIndexes []SearchIndexSpec     `json:"searchIndexes"`
	VectorIndexes []VectorIndexSpec     `json:"vectorIndexes"`
}

// ValidatorSchemaObject is the concrete JSON Schema shape emitted for table
// validators. Always type=object with strict additionalProperties=false.
type ValidatorSchemaObject struct {
	Type                 string                     `json:"type"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Required             []string                   `json:"required"`
	AdditionalProperties bool                       `json:"additionalProperties"`
}

// IndexSpec — a compound btree index on a NoSQL table.
type IndexSpec struct {
	Name   string   `json:"name"`
	Fields []string `json:"fields"`
}

// SearchIndexSpec — a tsvector search index.
type SearchIndexSpec struct {
	Name         string   `json:"name"`
	SearchField  string   `json:"searchField"`
	FilterFields []string `json:"filterFields"`
}

// VectorIndexSpec — a pgvector ivfflat index.
type VectorIndexSpec struct {
	Name         string   `json:"name"`
	VectorField  string   `json:"vectorField"`
	Dimensions   int      `json:"dimensions"`
	FilterFields []string `json:"filterFields"`
}

// defineSchemaPattern is a coarse heuristic. If the bundle does not reference
// defineSchema at all, we skip the JS evaluation entirely — saves the goja
// startup cost on every deploy of a plain-function bundle.
var defineSchemaPattern = regexp.MustCompile(`defineSchema\s*\(`)

// schemaSlotInit primes the side-channel slot so a bundle that does not call
// defineSchema returns a clean null rather than tripping ReferenceError when
// we read it back. The lib (and the user's defineSchema call) overwrite it.
const schemaSlotInit = "globalThis = globalThis || {};\n" +
	"globalThis.__excalibase_schema = null;\n"

// ExtractSchema runs the bundled code in an isolated goja VM, reads
// globalThis.__excalibase_schema, and returns the parsed Schema.
//
// Returns (schema, true, nil) when extraction succeeded.
// Returns (zero, false, nil) when the bundle did not call defineSchema —
// callers treat this as "no schema, continue deploy".
// Returns (zero, false, err) on a hard failure (malformed JS, JSON parse, etc.).
func ExtractSchema(bundleCode string) (Schema, bool, error) {
	if !defineSchemaPattern.MatchString(bundleCode) {
		return Schema{}, false, nil
	}
	vm := goja.New()
	// Provide a minimal globalThis. goja exposes one by default but the
	// preamble assignment fails on a missing field, so we make it explicit.
	if _, err := vm.RunString(schemaSlotInit); err != nil {
		return Schema{}, false, fmt.Errorf("init schema slot: %w", err)
	}
	// Phase 9b.F: bundles are now ESM. Goja parses ES5 only and rejects
	// import/export keywords. Strip them so the bundle body — where the
	// globalThis.__excalibase_schema assignment lives — is still parsable.
	stripped := stripESMForGoja(bundleCode)
	if _, err := vm.RunString(stripped); err != nil {
		return Schema{}, false, fmt.Errorf("evaluate bundle for schema extraction: %w", err)
	}
	val := vm.Get("__excalibase_schema")
	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return Schema{}, false, nil
	}
	raw, err := json.Marshal(val.Export())
	if err != nil {
		return Schema{}, false, fmt.Errorf("marshal extracted schema: %w", err)
	}
	if string(raw) == "null" {
		return Schema{}, false, nil
	}
	var schema Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return Schema{}, false, fmt.Errorf("parse extracted schema: %w", err)
	}
	return schema, true, nil
}
