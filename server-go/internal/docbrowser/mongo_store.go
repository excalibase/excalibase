package docbrowser

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoStore is Store over the official driver.
type mongoStore struct {
	client *mongo.Client
}

func (m *mongoStore) collection(ns Namespace) *mongo.Collection {
	return m.client.Database(ns.Database).Collection(ns.Collection)
}

func (m *mongoStore) ListDatabases(ctx context.Context) ([]string, error) {
	return m.client.ListDatabaseNames(ctx, bson.D{})
}

func (m *mongoStore) ListCollections(ctx context.Context, database string) ([]Collection, error) {
	specs, err := m.client.Database(database).ListCollectionSpecifications(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	collections := make([]Collection, 0, len(specs))
	for _, spec := range specs {
		collections = append(collections, Collection{Name: spec.Name, Type: spec.Type})
	}
	return collections, nil
}

func (m *mongoStore) CreateCollection(ctx context.Context, ns Namespace) error {
	return m.client.Database(ns.Database).CreateCollection(ctx, ns.Collection)
}

func (m *mongoStore) DropCollection(ctx context.Context, ns Namespace) error {
	return m.collection(ns).Drop(ctx)
}

func (m *mongoStore) Find(ctx context.Context, ns Namespace, query FindQuery) ([]bson.Raw, error) {
	opts := options.Find().SetLimit(query.Limit).SetSkip(query.Skip)
	if len(query.Sort) > 0 {
		opts.SetSort(query.Sort)
	}
	if len(query.Projection) > 0 {
		opts.SetProjection(query.Projection)
	}
	cursor, err := m.collection(ns).Find(ctx, query.Filter, opts)
	if err != nil {
		return nil, err
	}
	return drain(ctx, cursor)
}

func (m *mongoStore) Count(ctx context.Context, ns Namespace, filter bson.D) (int64, error) {
	return m.collection(ns).CountDocuments(ctx, filter)
}

func (m *mongoStore) InsertOne(ctx context.Context, ns Namespace, doc bson.D) (any, error) {
	result, err := m.collection(ns).InsertOne(ctx, doc)
	if err != nil {
		return nil, err
	}
	return result.InsertedID, nil
}

func byID(id any) bson.D {
	return bson.D{{Key: "_id", Value: id}}
}

func (m *mongoStore) ReplaceOne(ctx context.Context, ns Namespace, id any, doc bson.D) (int64, error) {
	result, err := m.collection(ns).ReplaceOne(ctx, byID(id), doc)
	if err != nil {
		return 0, err
	}
	return result.MatchedCount, nil
}

func (m *mongoStore) UpdateOne(ctx context.Context, ns Namespace, id any, update bson.D) (int64, error) {
	result, err := m.collection(ns).UpdateOne(ctx, byID(id), update)
	if err != nil {
		return 0, err
	}
	return result.MatchedCount, nil
}

func (m *mongoStore) DeleteOne(ctx context.Context, ns Namespace, id any) (int64, error) {
	result, err := m.collection(ns).DeleteOne(ctx, byID(id))
	if err != nil {
		return 0, err
	}
	return result.DeletedCount, nil
}

func (m *mongoStore) ListIndexes(ctx context.Context, ns Namespace) ([]bson.Raw, error) {
	cursor, err := m.collection(ns).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	return drain(ctx, cursor)
}

func (m *mongoStore) CreateIndex(ctx context.Context, ns Namespace, spec IndexSpec) (string, error) {
	opts := options.Index()
	if spec.Name != "" {
		opts.SetName(spec.Name)
	}
	if spec.Unique {
		opts.SetUnique(true)
	}
	return m.collection(ns).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: spec.Keys, Options: opts})
}

func (m *mongoStore) DropIndex(ctx context.Context, ns Namespace, name string) error {
	return m.collection(ns).Indexes().DropOne(ctx, name)
}

func (m *mongoStore) Sample(ctx context.Context, ns Namespace, size int) ([]bson.Raw, error) {
	pipeline := mongo.Pipeline{{{Key: "$sample", Value: bson.D{{Key: "size", Value: size}}}}}
	cursor, err := m.collection(ns).Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	return drain(ctx, cursor)
}

func drain(ctx context.Context, cursor *mongo.Cursor) (docs []bson.Raw, err error) {
	defer func() {
		if closeErr := cursor.Close(ctx); err == nil {
			err = closeErr
		}
	}()
	docs = []bson.Raw{}
	for cursor.Next(ctx) {
		docs = append(docs, append(bson.Raw(nil), cursor.Current...))
	}
	return docs, cursor.Err()
}
