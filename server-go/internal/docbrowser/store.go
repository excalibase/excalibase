// Package docbrowser is Studio's document browser for DocumentDB projects. It
// reaches a project's gateway over the MongoDB wire protocol and offers a
// fixed set of operations; nothing here runs an arbitrary command.
package docbrowser

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Namespace names one collection.
type Namespace struct {
	Database   string
	Collection string
}

// Collection is one entry of a database's collection list.
type Collection struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// FindQuery is a parsed find: filter, sort and projection are documents, and
// limit and skip have already been bounded.
type FindQuery struct {
	Filter     bson.D
	Sort       bson.D
	Projection bson.D
	Limit      int64
	Skip       int64
}

// IndexSpec is an index to create.
type IndexSpec struct {
	Keys   bson.D
	Name   string
	Unique bool
}

// Store is every operation the browser performs against one project's gateway.
type Store interface {
	ListDatabases(ctx context.Context) ([]string, error)
	ListCollections(ctx context.Context, database string) ([]Collection, error)
	CreateCollection(ctx context.Context, ns Namespace) error
	DropCollection(ctx context.Context, ns Namespace) error
	Find(ctx context.Context, ns Namespace, query FindQuery) ([]bson.Raw, error)
	Count(ctx context.Context, ns Namespace, filter bson.D) (int64, error)
	InsertOne(ctx context.Context, ns Namespace, doc bson.D) (any, error)
	ReplaceOne(ctx context.Context, ns Namespace, id any, doc bson.D) (int64, error)
	UpdateOne(ctx context.Context, ns Namespace, id any, update bson.D) (int64, error)
	DeleteOne(ctx context.Context, ns Namespace, id any) (int64, error)
	ListIndexes(ctx context.Context, ns Namespace) ([]bson.Raw, error)
	CreateIndex(ctx context.Context, ns Namespace, spec IndexSpec) (string, error)
	DropIndex(ctx context.Context, ns Namespace, name string) error
	Sample(ctx context.Context, ns Namespace, size int) ([]bson.Raw, error)
}

// Connector hands out the Store for one project.
type Connector interface {
	Store(ctx context.Context, projectID string) (Store, error)
}
