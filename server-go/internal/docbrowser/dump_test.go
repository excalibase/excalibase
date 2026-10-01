package docbrowser

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// memorySink keeps each dumped file in memory.
type memorySink map[string][]byte

func (m memorySink) WriteFile(name string, write func(io.Writer) error) error {
	var buffer bytes.Buffer
	if err := write(&buffer); err != nil {
		return err
	}
	m[name] = buffer.Bytes()
	return nil
}

// EXC-531: an export carries every collection in mongodump's directory
// layout, so mongorestore (or any BSON reader) imports it back.
func TestDumpWritesEachCollectionAsBSONAndItsMetadata(t *testing.T) {
	_, store := fakeStoreClient(t)
	sink := memorySink{}

	if err := dumpDocuments(context.Background(), store.client, sink); err != nil {
		t.Fatalf("dump: %v", err)
	}

	want, err := bson.Marshal(bson.D{{Key: "_id", Value: int32(1)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sink["shop/orders.bson"]; !bytes.Equal(got, want) {
		t.Fatalf("shop/orders.bson = %x, want the raw document %x", got, want)
	}
	var metadata struct {
		CollectionName string           `json:"collectionName"`
		Type           string           `json:"type"`
		Indexes        []map[string]any `json:"indexes"`
	}
	if err := json.Unmarshal(sink["shop/orders.metadata.json"], &metadata); err != nil {
		t.Fatalf("metadata: %v (%s)", err, sink["shop/orders.metadata.json"])
	}
	if metadata.CollectionName != "orders" || metadata.Type != "collection" {
		t.Fatalf("metadata names %+v", metadata)
	}
	if len(metadata.Indexes) != 1 || metadata.Indexes[0]["name"] != "_id_" {
		t.Fatalf("metadata indexes %v", metadata.Indexes)
	}
}

func TestDumpFileNamesEscapeWhatAPathWouldReadAsADirectory(t *testing.T) {
	if got := dumpFileName("a/b", "c/d%e", ".bson"); got != "a%2Fb/c%2Fd%25e.bson" {
		t.Fatalf("dumpFileName = %q", got)
	}
}
