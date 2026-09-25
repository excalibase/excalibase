package docbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const testProject = "proj-doc"

var shop = Namespace{Database: "shop", Collection: "orders"}

func newTestService(store *fakeStore) *Service {
	return NewService(fakeConnector{store: store}, Options{})
}

func TestFindParsesExtendedJSONQueryParts(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store)

	_, err := svc.Find(context.Background(), testProject, shop, FindRequest{
		Filter:     `{"total": {"$gt": 5}, "_id": {"$oid": "65f000000000000000000001"}}`,
		Sort:       `{"total": -1}`,
		Projection: `{"total": 1}`,
		Skip:       40,
	})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(store.lastFind.Filter) != 2 || store.lastFind.Filter[0].Key != "total" {
		t.Errorf("filter: %v", store.lastFind.Filter)
	}
	if _, ok := store.lastFind.Filter[1].Value.(bson.ObjectID); !ok {
		t.Errorf("$oid did not become an ObjectID: %T", store.lastFind.Filter[1].Value)
	}
	if store.lastFind.Sort[0].Key != "total" || store.lastFind.Projection[0].Key != "total" {
		t.Errorf("sort/projection: %v %v", store.lastFind.Sort, store.lastFind.Projection)
	}
	if store.lastFind.Skip != 40 || store.lastFind.Limit != DefaultLimit {
		t.Errorf("skip/limit: %d/%d", store.lastFind.Skip, store.lastFind.Limit)
	}
	if !store.deadlineSet {
		t.Error("find ran without a deadline")
	}
}

func TestFindCapsTheLimit(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store)

	page, err := svc.Find(context.Background(), testProject, shop, FindRequest{Limit: 5000})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if store.lastFind.Limit != MaxLimit || page.Limit != MaxLimit {
		t.Errorf("limit: store %d page %d, want %d", store.lastFind.Limit, page.Limit, MaxLimit)
	}
	if store.lastFind.Filter == nil {
		t.Error("an empty filter must be an empty document, not nil")
	}
}

func TestFindRejectsBadInput(t *testing.T) {
	cases := map[string]FindRequest{
		"negative skip":      {Skip: -1},
		"filter not object":  {Filter: `[1,2]`},
		"filter not json":    {Filter: `{total: 5}`},
		"sort not object":    {Sort: `"total"`},
		"projection garbage": {Projection: `{`},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{}
			_, err := newTestService(store).Find(context.Background(), testProject, shop, req)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v, want ErrInvalid", err)
			}
			if len(store.calls) != 0 {
				t.Errorf("the gateway was asked anyway: %v", store.calls)
			}
		})
	}
}

// Canonical, so Studio can edit a document and send it back without an int64
// or a whole-valued double quietly becoming an int32.
func TestFindReturnsCanonicalExtendedJSON(t *testing.T) {
	id := bson.NewObjectID()
	store := &fakeStore{documents: []bson.Raw{rawDoc(bson.D{{Key: "_id", Value: id}, {Key: "n", Value: int32(3)}})}}

	page, err := newTestService(store).Find(context.Background(), testProject, shop, FindRequest{})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	got := string(page.Documents[0])
	want := `{"_id":{"$oid":"` + id.Hex() + `"},"n":{"$numberInt":"3"}}`
	if got != want {
		t.Errorf("document:\n got %s\nwant %s", got, want)
	}
}

func TestNamesAreValidated(t *testing.T) {
	bad := []Namespace{
		{Database: "admin", Collection: "c"},
		{Database: "local", Collection: "c"},
		{Database: "config", Collection: "c"},
		{Database: "", Collection: "c"},
		{Database: "a/b", Collection: "c"},
		{Database: "a.b", Collection: "c"},
		{Database: strings.Repeat("d", 64), Collection: "c"},
		{Database: "shop", Collection: ""},
		{Database: "shop", Collection: "system.users"},
		{Database: "shop", Collection: "a$b"},
		{Database: "shop", Collection: "a\x00b"},
		{Database: "shop", Collection: strings.Repeat("c", 201)},
	}
	for _, ns := range bad {
		store := &fakeStore{}
		err := newTestService(store).DropCollection(context.Background(), testProject, ns)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%q.%q: got %v, want ErrInvalid", ns.Database, ns.Collection, err)
		}
		if len(store.calls) != 0 {
			t.Errorf("%q.%q reached the gateway", ns.Database, ns.Collection)
		}
	}
}

func TestListCollectionsValidatesTheDatabaseOnly(t *testing.T) {
	store := &fakeStore{collections: []Collection{{Name: "orders", Type: "collection"}}}
	svc := newTestService(store)

	got, err := svc.ListCollections(context.Background(), testProject, "shop")
	if err != nil || len(got) != 1 {
		t.Fatalf("ListCollections: %v %v", got, err)
	}
	if _, err := svc.ListCollections(context.Background(), testProject, "admin"); !errors.Is(err, ErrInvalid) {
		t.Errorf("admin: got %v, want ErrInvalid", err)
	}
}

func TestListDatabasesHidesTheSystemDatabases(t *testing.T) {
	store := &fakeStore{databases: []string{"admin", "shop", "config", "local", "crm"}}

	got, err := newTestService(store).ListDatabases(context.Background(), testProject)
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	if strings.Join(got, ",") != "shop,crm" {
		t.Errorf("databases: %v", got)
	}
}

func TestListDatabasesNeverReturnsNil(t *testing.T) {
	got, err := newTestService(&fakeStore{}).ListDatabases(context.Background(), testProject)
	if err != nil || got == nil {
		t.Fatalf("got %v %v, want an empty list", got, err)
	}
}

func TestCreateCollectionValidatesAndCreates(t *testing.T) {
	store := &fakeStore{}
	if err := newTestService(store).CreateCollection(context.Background(), testProject, shop); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if store.lastNS != shop || store.calls[0] != "createCollection" {
		t.Errorf("calls %v ns %v", store.calls, store.lastNS)
	}
}

func TestCountParsesTheFilter(t *testing.T) {
	store := &fakeStore{count: 42}
	got, err := newTestService(store).Count(context.Background(), testProject, shop, `{"a": 1}`)
	if err != nil || got != 42 {
		t.Fatalf("Count: %d %v", got, err)
	}
	if store.lastFilter[0].Key != "a" {
		t.Errorf("filter: %v", store.lastFilter)
	}
	if _, err := newTestService(store).Count(context.Background(), testProject, shop, `nope`); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad filter: got %v", err)
	}
}

func TestInsertReturnsTheIDAsExtendedJSON(t *testing.T) {
	id := bson.NewObjectID()
	store := &fakeStore{insertedID: id}

	got, err := newTestService(store).Insert(context.Background(), testProject, shop, []byte(`{"n": {"$numberLong": "7"}}`))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if string(got) != `{"$oid":"`+id.Hex()+`"}` {
		t.Errorf("id: %s", got)
	}
	if _, ok := store.lastDoc[0].Value.(int64); !ok {
		t.Errorf("$numberLong did not become int64: %T", store.lastDoc[0].Value)
	}
}

func TestInsertRejectsANonDocument(t *testing.T) {
	for _, body := range []string{`[]`, `5`, ``, `{`} {
		store := &fakeStore{}
		_, err := newTestService(store).Insert(context.Background(), testProject, shop, []byte(body))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: got %v, want ErrInvalid", body, err)
		}
	}
}

func TestReplaceTargetsTheParsedID(t *testing.T) {
	id := bson.NewObjectID()
	store := &fakeStore{matched: 1}
	idJSON := `{"$oid":"` + id.Hex() + `"}`

	err := newTestService(store).Replace(context.Background(), testProject, shop, idJSON,
		[]byte(`{"_id": `+idJSON+`, "n": 2}`))
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if store.lastID != id {
		t.Errorf("id: %v", store.lastID)
	}
}

func TestReplaceRefusesToChangeTheID(t *testing.T) {
	store := &fakeStore{matched: 1}
	err := newTestService(store).Replace(context.Background(), testProject, shop, `"a"`, []byte(`{"_id": "b"}`))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

func TestReplaceOfAMissingDocumentIsNotFound(t *testing.T) {
	store := &fakeStore{matched: 0}
	err := newTestService(store).Replace(context.Background(), testProject, shop, `"a"`, []byte(`{"n": 1}`))
	if !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("got %v, want ErrDocumentNotFound", err)
	}
}

func TestIDMustBeExtendedJSON(t *testing.T) {
	store := &fakeStore{matched: 1}
	svc := newTestService(store)
	for _, id := range []string{``, `{"$oid": "zz"}`, `not json`} {
		if err := svc.Delete(context.Background(), testProject, shop, id); !errors.Is(err, ErrInvalid) {
			t.Errorf("id %q: got %v, want ErrInvalid", id, err)
		}
	}
	if err := svc.Delete(context.Background(), testProject, shop, `7`); err != nil {
		t.Errorf("numeric id: %v", err)
	}
	if store.lastID != int32(7) {
		t.Errorf("numeric id became %T %v", store.lastID, store.lastID)
	}
}

func TestUpdateAcceptsOnlyOperators(t *testing.T) {
	store := &fakeStore{matched: 1}
	svc := newTestService(store)

	if err := svc.Update(context.Background(), testProject, shop, `"a"`, []byte(`{"$set": {"n": 1}}`)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	for _, body := range []string{`{"n": 1}`, `{}`, `{"$set": {"n": 1}, "m": 2}`} {
		if err := svc.Update(context.Background(), testProject, shop, `"a"`, []byte(body)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", body, err)
		}
	}
}

func TestUpdateAndDeleteOfAMissingDocumentIsNotFound(t *testing.T) {
	store := &fakeStore{matched: 0}
	svc := newTestService(store)
	if err := svc.Update(context.Background(), testProject, shop, `"a"`, []byte(`{"$set": {"n": 1}}`)); !errors.Is(err, ErrDocumentNotFound) {
		t.Errorf("update: %v", err)
	}
	if err := svc.Delete(context.Background(), testProject, shop, `"a"`); !errors.Is(err, ErrDocumentNotFound) {
		t.Errorf("delete: %v", err)
	}
}

func TestIndexes(t *testing.T) {
	store := &fakeStore{indexName: "total_-1", indexes: []bson.Raw{rawDoc(bson.D{{Key: "name", Value: "_id_"}})}}
	svc := newTestService(store)

	list, err := svc.ListIndexes(context.Background(), testProject, shop)
	if err != nil || string(list[0]) != `{"name":"_id_"}` {
		t.Fatalf("ListIndexes: %s %v", list, err)
	}
	name, err := svc.CreateIndex(context.Background(), testProject, shop, []byte(`{"keys": {"total": -1, "loc": "2dsphere"}, "unique": true}`))
	if err != nil || name != "total_-1" {
		t.Fatalf("CreateIndex: %q %v", name, err)
	}
	if !store.lastIndex.Unique || len(store.lastIndex.Keys) != 2 {
		t.Errorf("spec: %+v", store.lastIndex)
	}
	if err := svc.DropIndex(context.Background(), testProject, shop, "total_-1"); err != nil || store.droppedIdx != "total_-1" {
		t.Errorf("DropIndex: %v", err)
	}
}

func TestIndexInputIsValidated(t *testing.T) {
	svc := newTestService(&fakeStore{})
	for _, body := range []string{`{}`, `{"keys": {}}`, `{"keys": {"a": 2}}`, `{"keys": {"a": true}}`, `{"keys": []}`, `nope`} {
		if _, err := svc.CreateIndex(context.Background(), testProject, shop, []byte(body)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", body, err)
		}
	}
	for _, name := range []string{"_id_", "", strings.Repeat("i", 129)} {
		if err := svc.DropIndex(context.Background(), testProject, shop, name); !errors.Is(err, ErrInvalid) {
			t.Errorf("drop %q: got %v, want ErrInvalid", name, err)
		}
	}
}

func TestSampleTakesTenAndIsCachedBriefly(t *testing.T) {
	store := &fakeStore{documents: []bson.Raw{rawDoc(bson.D{{Key: "a", Value: 1}})}}
	now := time.Unix(1_700_000_000, 0)
	svc := NewService(fakeConnector{store: store}, Options{SampleTTL: time.Minute, Now: func() time.Time { return now }})

	for range 3 {
		docs, err := svc.Sample(context.Background(), testProject, shop)
		if err != nil || len(docs) != 1 {
			t.Fatalf("Sample: %v %v", docs, err)
		}
	}
	if store.sampleCalls != 1 || store.sampleSize != SampleSize {
		t.Errorf("sample calls %d size %d", store.sampleCalls, store.sampleSize)
	}
	now = now.Add(2 * time.Minute)
	if _, err := svc.Sample(context.Background(), testProject, shop); err != nil {
		t.Fatal(err)
	}
	if store.sampleCalls != 2 {
		t.Errorf("an expired sample was served: %d calls", store.sampleCalls)
	}
}

func TestAWriteDropsTheCachedSample(t *testing.T) {
	store := &fakeStore{documents: []bson.Raw{rawDoc(bson.D{{Key: "a", Value: 1}})}, insertedID: "x"}
	svc := newTestService(store)

	if _, err := svc.Sample(context.Background(), testProject, shop); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Insert(context.Background(), testProject, shop, []byte(`{"b": 1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sample(context.Background(), testProject, shop); err != nil {
		t.Fatal(err)
	}
	if store.sampleCalls != 2 {
		t.Errorf("the sample survived a write: %d calls", store.sampleCalls)
	}
}

func TestSampleCacheIsPerProject(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store)
	_, _ = svc.Sample(context.Background(), "proj-a", shop)
	_, _ = svc.Sample(context.Background(), "proj-b", shop)
	if store.sampleCalls != 2 {
		t.Errorf("one project's sample was served to another: %d calls", store.sampleCalls)
	}
}

func TestSampleCacheIsBounded(t *testing.T) {
	cache := newSampleCache(time.Minute, time.Now)
	for i := range maxCachedSamples + 10 {
		cache.put(sampleKey{project: "p", ns: Namespace{Database: "d", Collection: string(rune('a'+i%26)) + strings.Repeat("x", i)}}, nil)
	}
	if len(cache.entries) > maxCachedSamples {
		t.Errorf("cache holds %d entries, cap %d", len(cache.entries), maxCachedSamples)
	}
}

func TestGatewayFailuresHideTheAddress(t *testing.T) {
	store := &fakeStore{err: errFakeNetwork}
	_, err := newTestService(store).ListDatabases(context.Background(), testProject)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
	if strings.Contains(PublicMessage(err), "gateway.internal") {
		t.Errorf("the public message leaks the address: %s", PublicMessage(err))
	}
}

func TestDeadlinesBecomeTimeouts(t *testing.T) {
	store := &fakeStore{err: context.DeadlineExceeded}
	_, err := newTestService(store).ListDatabases(context.Background(), testProject)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v, want ErrTimeout", err)
	}
}

func TestServerErrorsAreShownAsQueryErrors(t *testing.T) {
	store := &fakeStore{err: mongo.CommandError{Code: 2, Message: "unknown operator: $gtx"}}
	_, err := newTestService(store).Find(context.Background(), testProject, shop, FindRequest{})
	var queryErr *QueryError
	if !errors.As(err, &queryErr) || queryErr.Conflict {
		t.Fatalf("got %v, want a QueryError", err)
	}
	if PublicMessage(err) != "unknown operator: $gtx" {
		t.Errorf("message: %q", PublicMessage(err))
	}
}

func TestDuplicateKeysAreConflicts(t *testing.T) {
	dup := mongo.WriteException{WriteErrors: []mongo.WriteError{{Code: 11000, Message: "E11000 duplicate key"}}}
	store := &fakeStore{err: dup}
	_, err := newTestService(store).Insert(context.Background(), testProject, shop, []byte(`{"_id": 1}`))
	var queryErr *QueryError
	if !errors.As(err, &queryErr) || !queryErr.Conflict {
		t.Fatalf("got %v, want a conflicting QueryError", err)
	}
}

func TestConnectorRefusalsPassThrough(t *testing.T) {
	svc := NewService(fakeConnector{err: ErrNotDocumentDB}, Options{})
	if _, err := svc.ListDatabases(context.Background(), testProject); !errors.Is(err, ErrNotDocumentDB) {
		t.Errorf("got %v, want ErrNotDocumentDB", err)
	}
}

func TestPageSerialises(t *testing.T) {
	page := Page{Documents: []json.RawMessage{json.RawMessage(`{"a":1}`)}, Limit: 20}
	out, err := json.Marshal(page)
	if err != nil || string(out) != `{"documents":[{"a":1}],"limit":20,"skip":0}` {
		t.Errorf("page: %s %v", out, err)
	}
}

func TestEveryOperationValidatesItsNamespace(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store)
	ctx := context.Background()
	bad := Namespace{Database: "admin", Collection: "c"}
	calls := map[string]func() error{
		"create":      func() error { return svc.CreateCollection(ctx, testProject, bad) },
		"find":        func() error { _, err := svc.Find(ctx, testProject, bad, FindRequest{}); return err },
		"count":       func() error { _, err := svc.Count(ctx, testProject, bad, ""); return err },
		"insert":      func() error { _, err := svc.Insert(ctx, testProject, bad, []byte(`{}`)); return err },
		"replace":     func() error { return svc.Replace(ctx, testProject, bad, `1`, []byte(`{}`)) },
		"update":      func() error { return svc.Update(ctx, testProject, bad, `1`, []byte(`{"$set":{}}`)) },
		"delete":      func() error { return svc.Delete(ctx, testProject, bad, `1`) },
		"listIndexes": func() error { _, err := svc.ListIndexes(ctx, testProject, bad); return err },
		"createIndex": func() error { _, err := svc.CreateIndex(ctx, testProject, bad, []byte(`{"keys":{"a":1}}`)); return err },
		"dropIndex":   func() error { return svc.DropIndex(ctx, testProject, bad, "a_1") },
		"sample":      func() error { _, err := svc.Sample(ctx, testProject, bad); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if len(store.calls) != 0 {
		t.Errorf("reached the gateway: %v", store.calls)
	}
}

func TestEveryOperationReportsTheGatewaysFailure(t *testing.T) {
	store := &fakeStore{err: errFakeNetwork, matched: 1}
	svc := newTestService(store)
	ctx := context.Background()
	calls := map[string]func() error{
		"collections": func() error { _, err := svc.ListCollections(ctx, testProject, "shop"); return err },
		"create":      func() error { return svc.CreateCollection(ctx, testProject, shop) },
		"drop":        func() error { return svc.DropCollection(ctx, testProject, shop) },
		"find":        func() error { _, err := svc.Find(ctx, testProject, shop, FindRequest{}); return err },
		"count":       func() error { _, err := svc.Count(ctx, testProject, shop, ""); return err },
		"insert":      func() error { _, err := svc.Insert(ctx, testProject, shop, []byte(`{}`)); return err },
		"replace":     func() error { return svc.Replace(ctx, testProject, shop, `1`, []byte(`{}`)) },
		"listIndexes": func() error { _, err := svc.ListIndexes(ctx, testProject, shop); return err },
		"createIndex": func() error {
			_, err := svc.CreateIndex(ctx, testProject, shop, []byte(`{"keys":{"a":1}}`))
			return err
		},
		"dropIndex": func() error { return svc.DropIndex(ctx, testProject, shop, "a_1") },
		"sample":    func() error { _, err := svc.Sample(ctx, testProject, shop); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: got %v, want ErrUnavailable", name, err)
		}
	}
}

func TestTargetedWritesValidateTheIDAndBody(t *testing.T) {
	svc := newTestService(&fakeStore{matched: 1})
	ctx := context.Background()
	if err := svc.Replace(ctx, testProject, shop, ``, []byte(`{}`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("replace without id: %v", err)
	}
	if err := svc.Update(ctx, testProject, shop, `1`, []byte(`[]`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("update with a non-document: %v", err)
	}
}

func TestDropCollectionDropsAndForgetsTheSample(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store)
	if _, err := svc.Sample(context.Background(), testProject, shop); err != nil {
		t.Fatal(err)
	}
	if err := svc.DropCollection(context.Background(), testProject, shop); err != nil {
		t.Fatalf("DropCollection: %v", err)
	}
	if _, err := svc.Sample(context.Background(), testProject, shop); err != nil {
		t.Fatal(err)
	}
	if store.sampleCalls != 2 {
		t.Errorf("a dropped collection's sample was served: %d calls", store.sampleCalls)
	}
}

func TestPublicMessages(t *testing.T) {
	cases := map[error]string{
		ErrGatewayNotReady:                   ErrGatewayNotReady.Error(),
		ErrNotDocumentDB:                     ErrNotDocumentDB.Error(),
		ErrDocumentNotFound:                  ErrDocumentNotFound.Error(),
		invalid("bad %s", "thing"):           "invalid request: bad thing",
		errors.New("vault path projects/x"):  "the document browser failed",
		&QueryError{Message: "bad operator"}: "bad operator",
	}
	for err, want := range cases {
		if got := PublicMessage(err); got != want {
			t.Errorf("%v: got %q, want %q", err, got, want)
		}
	}
	if (&QueryError{Message: "m"}).Error() != "m" {
		t.Error("QueryError.Error")
	}
	if serverMessage(errors.New("plain")) != "plain" {
		t.Error("serverMessage of a plain error")
	}
}

func TestIndexKeysAcceptLongDirections(t *testing.T) {
	spec, err := parseIndexSpec([]byte(`{"keys": {"a": {"$numberLong": "-1"}, "b": "hashed"}}`))
	if err != nil || len(spec.Keys) != 2 {
		t.Fatalf("spec: %+v %v", spec, err)
	}
	if _, err := parseIndexSpec([]byte(`{"keys": {"a": 1}, "name": "_id_"}`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("reserved name: %v", err)
	}
}

func TestNewServiceDefaults(t *testing.T) {
	svc := NewService(fakeConnector{store: &fakeStore{}}, Options{})
	if svc.timeout != defaultTimeout || svc.samples.ttl != defaultSampleTTL {
		t.Errorf("defaults: %v %v", svc.timeout, svc.samples.ttl)
	}
	connector := NewGatewayConnector(GatewayConnectorConfig{})
	if connector.timeout != defaultTimeout || connector.maxClients != defaultMaxClients {
		t.Errorf("connector defaults: %v %d", connector.timeout, connector.maxClients)
	}
}

// The form Studio's editor writes: plain numbers where they read back as the
// same type, wrappers where they would not.
func TestEditedDocumentsKeepTheirTypes(t *testing.T) {
	doc, err := parseDocument("doc", []byte(`{"a": 5, "b": {"$numberDouble": "5.0"}, "c": {"$numberLong": "7"},
		"d": 3000000000, "e": 1.5, "f": {"$date": "2024-01-01T00:00:00.000Z"}}`), false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"int32", "float64", "int64", "int64", "float64", "bson.DateTime"}
	for i, field := range doc {
		if got := fmt.Sprintf("%T", field.Value); got != want[i] {
			t.Errorf("%s: %s, want %s", field.Key, got, want[i])
		}
	}
}
