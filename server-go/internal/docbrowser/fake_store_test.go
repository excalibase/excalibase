package docbrowser

import (
	"context"
	"errors"
	"sync"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// fakeStore records what the service asked of the gateway and answers from
// fields the test sets.
type fakeStore struct {
	mu sync.Mutex

	databases   []string
	collections []Collection
	documents   []bson.Raw
	indexes     []bson.Raw
	count       int64
	insertedID  any
	matched     int64
	indexName   string
	err         error

	lastFind    FindQuery
	lastFilter  bson.D
	lastDoc     bson.D
	lastID      any
	lastNS      Namespace
	lastIndex   IndexSpec
	droppedIdx  string
	sampleCalls int
	sampleSize  int
	calls       []string
	deadlineSet bool
}

func (f *fakeStore) record(ctx context.Context, call string, ns Namespace) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.lastNS = ns
	_, f.deadlineSet = ctx.Deadline()
	return f.err
}

func (f *fakeStore) ListDatabases(ctx context.Context) ([]string, error) {
	return f.databases, f.record(ctx, "listDatabases", Namespace{})
}

func (f *fakeStore) ListCollections(ctx context.Context, database string) ([]Collection, error) {
	return f.collections, f.record(ctx, "listCollections", Namespace{Database: database})
}

func (f *fakeStore) CreateCollection(ctx context.Context, ns Namespace) error {
	return f.record(ctx, "createCollection", ns)
}

func (f *fakeStore) DropCollection(ctx context.Context, ns Namespace) error {
	return f.record(ctx, "dropCollection", ns)
}

func (f *fakeStore) Find(ctx context.Context, ns Namespace, query FindQuery) ([]bson.Raw, error) {
	f.lastFind = query
	return f.documents, f.record(ctx, "find", ns)
}

func (f *fakeStore) Count(ctx context.Context, ns Namespace, filter bson.D) (int64, error) {
	f.lastFilter = filter
	return f.count, f.record(ctx, "count", ns)
}

func (f *fakeStore) InsertOne(ctx context.Context, ns Namespace, doc bson.D) (any, error) {
	f.lastDoc = doc
	return f.insertedID, f.record(ctx, "insertOne", ns)
}

func (f *fakeStore) ReplaceOne(ctx context.Context, ns Namespace, id any, doc bson.D) (int64, error) {
	f.lastID, f.lastDoc = id, doc
	return f.matched, f.record(ctx, "replaceOne", ns)
}

func (f *fakeStore) UpdateOne(ctx context.Context, ns Namespace, id any, update bson.D) (int64, error) {
	f.lastID, f.lastDoc = id, update
	return f.matched, f.record(ctx, "updateOne", ns)
}

func (f *fakeStore) DeleteOne(ctx context.Context, ns Namespace, id any) (int64, error) {
	f.lastID = id
	return f.matched, f.record(ctx, "deleteOne", ns)
}

func (f *fakeStore) ListIndexes(ctx context.Context, ns Namespace) ([]bson.Raw, error) {
	return f.indexes, f.record(ctx, "listIndexes", ns)
}

func (f *fakeStore) CreateIndex(ctx context.Context, ns Namespace, spec IndexSpec) (string, error) {
	f.lastIndex = spec
	return f.indexName, f.record(ctx, "createIndex", ns)
}

func (f *fakeStore) DropIndex(ctx context.Context, ns Namespace, name string) error {
	f.droppedIdx = name
	return f.record(ctx, "dropIndex", ns)
}

func (f *fakeStore) Sample(ctx context.Context, ns Namespace, size int) ([]bson.Raw, error) {
	f.sampleCalls++
	f.sampleSize = size
	return f.documents, f.record(ctx, "sample", ns)
}

// fakeConnector hands out one store, or refuses with err.
type fakeConnector struct {
	store *fakeStore
	err   error
}

func (c fakeConnector) Store(_ context.Context, _ string) (Store, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.store, nil
}

var errFakeNetwork = errors.New("dial tcp gateway.internal:10260: connection refused")

func rawDoc(doc bson.D) bson.Raw {
	raw, err := bson.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
}
