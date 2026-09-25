package docbrowser

import (
	"bytes"
	"encoding/json"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	maxDatabaseName   = 63
	maxCollectionName = 200
	maxIndexName      = 128
	defaultIndexName  = "_id_"
)

// systemDatabases hold the gateway's own state; the browser neither shows
// nor touches them.
var systemDatabases = map[string]bool{"admin": true, "local": true, "config": true}

func validateDatabase(name string) error {
	if name == "" || len(name) > maxDatabaseName {
		return invalid("a database name must be 1 to %d characters", maxDatabaseName)
	}
	if strings.ContainsAny(name, "/\\. \"$*<>:|?\x00") {
		return invalid("database name %q contains a character MongoDB does not allow", name)
	}
	if systemDatabases[name] {
		return invalid("database %q is reserved", name)
	}
	return nil
}

func validateNamespace(ns Namespace) error {
	if err := validateDatabase(ns.Database); err != nil {
		return err
	}
	name := ns.Collection
	if name == "" || len(name) > maxCollectionName {
		return invalid("a collection name must be 1 to %d characters", maxCollectionName)
	}
	if strings.ContainsAny(name, "$\x00") {
		return invalid("collection name %q contains a character MongoDB does not allow", name)
	}
	if strings.HasPrefix(name, "system.") {
		return invalid("collection %q is reserved", name)
	}
	return nil
}

func validateIndexName(name string) error {
	if name == "" || len(name) > maxIndexName || strings.ContainsRune(name, 0) {
		return invalid("an index name must be 1 to %d characters", maxIndexName)
	}
	if name == defaultIndexName {
		return invalid("the _id index cannot be dropped")
	}
	return nil
}

// parseDocument reads one Extended JSON object. Empty input is an empty
// document when optional.
func parseDocument(what string, data []byte, optional bool) (bson.D, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 && optional {
		return bson.D{}, nil
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, invalid("%s must be a JSON object", what)
	}
	doc := bson.D{}
	if err := bson.UnmarshalExtJSON(trimmed, false, &doc); err != nil {
		return nil, invalid("%s is not valid Extended JSON: %v", what, err)
	}
	return doc, nil
}

// parseID reads an _id value written as Extended JSON: {"$oid": "..."},
// "a string", 7, and so on.
func parseID(data string) (any, error) {
	if strings.TrimSpace(data) == "" {
		return nil, invalid("a document id is required")
	}
	wrapped := bson.D{}
	if err := bson.UnmarshalExtJSON([]byte(`{"_id":`+data+`}`), false, &wrapped); err != nil || len(wrapped) != 1 {
		return nil, invalid("the document id is not valid Extended JSON")
	}
	return wrapped[0].Value, nil
}

func sameValue(a, b any) bool {
	left, errLeft := bson.Marshal(bson.D{{Key: "v", Value: a}})
	right, errRight := bson.Marshal(bson.D{{Key: "v", Value: b}})
	return errLeft == nil && errRight == nil && bytes.Equal(left, right)
}

func toExtJSON(raw bson.Raw) (json.RawMessage, error) {
	out, err := bson.MarshalExtJSON(raw, true, false)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func valueToExtJSON(value any) (json.RawMessage, error) {
	out, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: value}}, true, false)
	if err != nil {
		return nil, err
	}
	var wrapped struct {
		V json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(out, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.V, nil
}

type indexRequest struct {
	Keys   json.RawMessage `json:"keys"`
	Name   string          `json:"name"`
	Unique bool            `json:"unique"`
}

// indexKinds are the string key types an index may name.
var indexKinds = map[string]bool{"text": true, "2dsphere": true, "2d": true, "hashed": true}

func parseIndexSpec(data []byte) (IndexSpec, error) {
	var req indexRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return IndexSpec{}, invalid("the index is not valid JSON")
	}
	keys, err := parseDocument("keys", req.Keys, false)
	if err != nil {
		return IndexSpec{}, err
	}
	if len(keys) == 0 {
		return IndexSpec{}, invalid("an index needs at least one key")
	}
	for _, key := range keys {
		if !validIndexDirection(key.Value) {
			return IndexSpec{}, invalid("index key %q must be 1, -1 or an index type", key.Key)
		}
	}
	if req.Name != "" {
		if err := validateIndexName(req.Name); err != nil {
			return IndexSpec{}, err
		}
	}
	return IndexSpec{Keys: keys, Name: req.Name, Unique: req.Unique}, nil
}

func validIndexDirection(value any) bool {
	switch v := value.(type) {
	case int32:
		return v == 1 || v == -1
	case int64:
		return v == 1 || v == -1
	case string:
		return indexKinds[v]
	}
	return false
}

func onlyOperators(update bson.D) bool {
	if len(update) == 0 {
		return false
	}
	for _, field := range update {
		if !strings.HasPrefix(field.Key, "$") {
			return false
		}
	}
	return true
}
