package docbrowser

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Every store operation reports a gateway it cannot reach rather than
// answering empty. The operations themselves are proved against a real
// gateway in the integration test.
func TestMongoStoreReportsAnUnreachableGateway(t *testing.T) {
	client, err := mongo.Connect(options.Client().
		SetHosts([]string{"gateway.invalid:10260"}).
		SetServerSelectionTimeout(50 * time.Millisecond).
		SetConnectTimeout(50 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Disconnect(context.Background()); err != nil {
			t.Logf("disconnect: %v", err)
		}
	})
	store := &mongoStore{client: client}
	ctx := context.Background()
	ns := Namespace{Database: "shop", Collection: "orders"}

	calls := map[string]func() error{
		"listDatabases":    func() error { _, err := store.ListDatabases(ctx); return err },
		"listCollections":  func() error { _, err := store.ListCollections(ctx, "shop"); return err },
		"createCollection": func() error { return store.CreateCollection(ctx, ns) },
		"dropCollection":   func() error { return store.DropCollection(ctx, ns) },
		"find": func() error {
			_, err := store.Find(ctx, ns, FindQuery{Filter: bson.D{}, Sort: bson.D{{Key: "a", Value: 1}}, Projection: bson.D{{Key: "a", Value: 1}}, Limit: 1})
			return err
		},
		"count":      func() error { _, err := store.Count(ctx, ns, bson.D{}); return err },
		"insertOne":  func() error { _, err := store.InsertOne(ctx, ns, bson.D{{Key: "a", Value: 1}}); return err },
		"replaceOne": func() error { _, err := store.ReplaceOne(ctx, ns, 1, bson.D{{Key: "a", Value: 1}}); return err },
		"updateOne": func() error {
			_, err := store.UpdateOne(ctx, ns, 1, bson.D{{Key: "$set", Value: bson.D{{Key: "a", Value: 1}}}})
			return err
		},
		"deleteOne":   func() error { _, err := store.DeleteOne(ctx, ns, 1); return err },
		"listIndexes": func() error { _, err := store.ListIndexes(ctx, ns); return err },
		"createIndex": func() error {
			_, err := store.CreateIndex(ctx, ns, IndexSpec{Keys: bson.D{{Key: "a", Value: 1}}, Name: "a", Unique: true})
			return err
		},
		"dropIndex": func() error { return store.DropIndex(ctx, ns, "a") },
		"sample":    func() error { _, err := store.Sample(ctx, ns, SampleSize); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s: an unreachable gateway answered", name)
		} else if !errors.Is(classify(err), ErrUnavailable) && !errors.Is(classify(err), ErrTimeout) {
			t.Errorf("%s: %v is not reported as unavailable", name, err)
		}
	}
}

func fakeStoreClient(t *testing.T) (*wireFake, *mongoStore) {
	t.Helper()
	fake := startWireFake(t)
	client, err := mongo.Connect(options.Client().
		SetHosts([]string{fake.address()}).
		SetDirect(true).
		SetServerAPIOptions(options.ServerAPI(options.ServerAPIVersion1)).
		SetRetryWrites(false).
		SetTimeout(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Disconnect(context.Background()); err != nil {
			t.Logf("disconnect: %v", err)
		}
	})
	return fake, &mongoStore{client: client}
}

func expect(ok bool, got any, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unexpected result %v", got)
	}
	return nil
}

func storeChecks(ctx context.Context, store *mongoStore) map[string]func() error {
	ns := Namespace{Database: "shop", Collection: "orders"}
	return map[string]func() error{
		"ListDatabases": func() error {
			got, err := store.ListDatabases(ctx)
			return expect(len(got) == 1 && got[0] == "shop", got, err)
		},
		"ListCollections": func() error {
			got, err := store.ListCollections(ctx, "shop")
			return expect(len(got) == 1 && got[0] == Collection{Name: "orders", Type: "collection"}, got, err)
		},
		"Find": func() error {
			got, err := store.Find(ctx, ns, FindQuery{Filter: bson.D{}, Sort: bson.D{{Key: "a", Value: 1}}, Projection: bson.D{{Key: "a", Value: 1}}, Limit: 5})
			return expect(len(got) == 1, got, err)
		},
		"Count": func() error {
			got, err := store.Count(ctx, ns, bson.D{})
			return expect(got == 2, got, err)
		},
		"Sample": func() error {
			got, err := store.Sample(ctx, ns, SampleSize)
			return expect(len(got) == 2, got, err)
		},
		"ListIndexes": func() error {
			got, err := store.ListIndexes(ctx, ns)
			return expect(len(got) == 1, got, err)
		},
		"InsertOne": func() error {
			got, err := store.InsertOne(ctx, ns, bson.D{{Key: "_id", Value: int32(9)}})
			return expect(got == int32(9), got, err)
		},
		"ReplaceOne": func() error {
			got, err := store.ReplaceOne(ctx, ns, 9, bson.D{{Key: "a", Value: 1}})
			return expect(got == 1, got, err)
		},
		"UpdateOne": func() error {
			got, err := store.UpdateOne(ctx, ns, 9, bson.D{{Key: "$set", Value: bson.D{{Key: "a", Value: 2}}}})
			return expect(got == 1, got, err)
		},
		"DeleteOne": func() error {
			got, err := store.DeleteOne(ctx, ns, 9)
			return expect(got == 1, got, err)
		},
		"CreateIndex": func() error {
			got, err := store.CreateIndex(ctx, ns, IndexSpec{Keys: bson.D{{Key: "a", Value: 1}}, Name: "by_a", Unique: true})
			return expect(got == "by_a", got, err)
		},
		"CreateCollection": func() error { return store.CreateCollection(ctx, ns) },
		"DropIndex":        func() error { return store.DropIndex(ctx, ns, "by_a") },
		"DropCollection":   func() error { return store.DropCollection(ctx, ns) },
	}
}

var offeredCommands = map[string]bool{
	"hello": true, "isMaster": true, "ismaster": true, "endSessions": true,
	"listDatabases": true, "listCollections": true, "find": true, "aggregate": true, "listIndexes": true,
	"insert": true, "update": true, "delete": true, "createIndexes": true, "dropIndexes": true, "create": true, "drop": true,
}

func TestMongoStoreSpeaksOnlyTheCommandsItOffers(t *testing.T) {
	fake, store := fakeStoreClient(t)
	for name, check := range storeChecks(context.Background(), store) {
		if err := check(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, command := range fake.seen() {
		if !offeredCommands[command] {
			t.Errorf("the store sent %q", command)
		}
	}
}
