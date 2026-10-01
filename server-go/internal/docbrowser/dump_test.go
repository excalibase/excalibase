package docbrowser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
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

type failingSink struct{}

func (failingSink) WriteFile(string, func(io.Writer) error) error { return errors.New("disk full") }

func TestTheConnectorDumpsTheProjectsGateway(t *testing.T) {
	fake := startWireFake(t)
	f := newConnectorFixture(t)
	c := f.connector(t)
	c.dial = func(*options.ClientOptions) (*mongo.Client, error) {
		return mongo.Connect(options.Client().SetHosts([]string{fake.address()}).SetDirect(true).
			SetServerAPIOptions(options.ServerAPI(options.ServerAPIVersion1)).SetRetryWrites(false).SetTimeout(5 * time.Second))
	}
	sink := memorySink{}
	if err := c.DumpDocuments(context.Background(), connProject, sink); err != nil {
		t.Fatalf("dump: %v", err)
	}
	if _, ok := sink["shop/orders.bson"]; !ok {
		t.Fatalf("files: %v", sink)
	}
}

func TestTheConnectorRefusesToDumpAProjectItCannotServe(t *testing.T) {
	f := newConnectorFixture(t)
	f.projects.inst.DocumentDB = false
	if err := f.connector(t).DumpDocuments(context.Background(), connProject, memorySink{}); !errors.Is(err, ErrNotDocumentDB) {
		t.Fatalf("got %v, want ErrNotDocumentDB", err)
	}
}

func TestADumpFailsWhenAFileCannotBeWritten(t *testing.T) {
	_, store := fakeStoreClient(t)
	if err := dumpDocuments(context.Background(), store.client, failingSink{}); err == nil {
		t.Fatal("a dump that could not write its files must fail")
	}
}

func TestADumpFailsWhenTheGatewayCannotBeReached(t *testing.T) {
	client, err := mongo.Connect(options.Client().SetHosts([]string{"gateway.invalid:10260"}).
		SetServerSelectionTimeout(50 * time.Millisecond).SetConnectTimeout(50 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	if err := dumpDocuments(context.Background(), client, memorySink{}); err == nil {
		t.Fatal("an unreachable gateway must fail the dump, not answer empty")
	}
}

func TestAViewIsDumpedAsMetadataOnly(t *testing.T) {
	fake, store := fakeStoreClient(t)
	options, err := bson.Marshal(bson.D{{Key: "viewOn", Value: "orders"}})
	if err != nil {
		t.Fatal(err)
	}
	view := store.client.Database("shop").Collection("big_orders")
	sink := memorySink{}
	spec := mongo.CollectionSpecification{Name: "big_orders", Type: "view", Options: options}
	if err := dumpMetadata(context.Background(), view, spec, sink); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	metadata := string(sink["shop/big_orders.metadata.json"])
	if !strings.Contains(metadata, `"viewOn":"orders"`) || !strings.Contains(metadata, `"type":"view"`) {
		t.Fatalf("view metadata: %s", metadata)
	}
	for _, command := range fake.seen() {
		if command == "listIndexes" {
			t.Fatal("a view has no indexes to list")
		}
	}
}
