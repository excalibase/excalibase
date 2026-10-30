package engineproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
)

// Docker and Podman match keys without case and keep the last twin, so a checked key could carry one
// the engine reads instead (EXC-681): twins are refused and the engine gets the proxy's own encoding.

// maxBodyDepth bounds nesting; the deepest field the API takes is a few levels down.
const maxBodyDepth = 32

var errTooDeep = errors.New("nested too deep")

// canonicalBody parses body strictly and answers the encoding of it the engine is sent.
func canonicalBody(body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := readValue(decoder, 0)
	if err != nil {
		return nil, refuse("malformed or ambiguous JSON: %v", err)
	}
	if _, isObject := value.(map[string]any); !isObject {
		return nil, refuse("JSON must be an object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, refuse("JSON holds more than one value")
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode the checked body: %w", err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func readValue(decoder *json.Decoder, depth int) (any, error) {
	if depth >= maxBodyDepth {
		return nil, errTooDeep
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		return readObject(decoder, depth)
	case json.Delim('['):
		return readArray(decoder, depth)
	}
	return token, nil
}

func readObject(decoder *json.Decoder, depth int) (map[string]any, error) {
	object := map[string]any{}
	seen := map[string]string{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, isKey := token.(string)
		if !isKey {
			return nil, fmt.Errorf("object key %v is not a string", token)
		}
		// Engines fold non-ASCII letters differently (Podman reads "İ" as "i", Go does not); no API key has one.
		if strings.ContainsFunc(key, func(char rune) bool { return char > unicode.MaxASCII }) {
			return nil, fmt.Errorf("key %q is not ASCII", key)
		}
		folded := foldName(key)
		if first, repeated := seen[folded]; repeated {
			return nil, fmt.Errorf("key %q repeats %q; the engine would read one of them without case", key, first)
		}
		seen[folded] = key
		if object[key], err = readValue(decoder, depth+1); err != nil {
			return nil, err
		}
	}
	_, err := decoder.Token()
	return object, err
}

func readArray(decoder *json.Decoder, depth int) ([]any, error) {
	array := []any{}
	for decoder.More() {
		value, err := readValue(decoder, depth+1)
		if err != nil {
			return nil, err
		}
		array = append(array, value)
	}
	_, err := decoder.Token()
	return array, err
}

// foldName folds a key as bytes.EqualFold compares, which is how encoding/json matches field
// names: every rune becomes the smallest of its case-folding orbit, so "ſ" and "S" fold alike.
func foldName(name string) string {
	var folded strings.Builder
	for _, char := range name {
		smallest := char
		for other := unicode.SimpleFold(char); other != char; other = unicode.SimpleFold(other) {
			smallest = min(smallest, other)
		}
		folded.WriteRune(smallest)
	}
	return folded.String()
}

// decodeSpelled decodes a checked body as an object whose fields among names are spelled exactly.
func decodeSpelled(what string, body []byte, names ...string) (map[string]any, error) {
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, refuse("%s body: %v", what, err)
	}
	return object, checkSpelling(what, object, names...)
}

// checkSpelling refuses a key that the engine would read as one of names but that the proxy,
// looking names up exactly, would not.
func checkSpelling(what string, object map[string]any, names ...string) error {
	for key := range object {
		for _, name := range names {
			if key != name && foldName(key) == foldName(name) {
				return refuse("%s field %q must be spelled %q", what, key, name)
			}
		}
	}
	return nil
}

// onlyFields refuses any field of object outside names that is set, so a field the proxy
// checks by name cannot arrive spelled another way, and nothing unchecked rides along.
func onlyFields(what string, object map[string]any, names ...string) error {
	for key, value := range object {
		if !isUnset(value) && !slices.Contains(names, key) {
			return refuse("%s field %s may not be set", what, key)
		}
	}
	return nil
}
