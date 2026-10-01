package docbrowser

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// A project's documents exported in mongodump's directory layout (EXC-531):
// <db>/<collection>.bson holds the documents back to back, exactly as the
// gateway returned them, and <db>/<collection>.metadata.json the options and
// indexes in canonical Extended JSON. mongorestore --dir reads it, and so can
// any BSON reader. Views carry metadata only, as mongodump writes them.

// FileWriter receives one dumped file per call; write streams its content.
type FileWriter interface {
	WriteFile(name string, write func(io.Writer) error) error
}

const viewType = "view"

// DumpDocuments writes every customer collection of the project through its
// gateway, as the document browser's login.
func (c *GatewayConnector) DumpDocuments(ctx context.Context, projectID string, sink FileWriter) error {
	store, err := c.Store(ctx, projectID)
	if err != nil {
		return err
	}
	mongoStore, ok := store.(*mongoStore)
	if !ok {
		return fmt.Errorf("the document store of %s cannot be dumped", projectID)
	}
	return dumpDocuments(ctx, mongoStore.client, sink)
}

func dumpDocuments(ctx context.Context, client *mongo.Client, sink FileWriter) error {
	databases, err := client.ListDatabaseNames(ctx, bson.D{})
	if err != nil {
		return fmt.Errorf("list databases: %w", err)
	}
	for _, name := range databases {
		if systemDatabases[name] {
			continue
		}
		if err := dumpDatabase(ctx, client.Database(name), sink); err != nil {
			return err
		}
	}
	return nil
}

func dumpDatabase(ctx context.Context, database *mongo.Database, sink FileWriter) error {
	specs, err := database.ListCollectionSpecifications(ctx, bson.D{})
	if err != nil {
		return fmt.Errorf("list collections of %s: %w", database.Name(), err)
	}
	for _, spec := range specs {
		if strings.HasPrefix(spec.Name, "system.") {
			continue
		}
		collection := database.Collection(spec.Name)
		if spec.Type != viewType {
			if err := dumpCollection(ctx, collection, sink); err != nil {
				return err
			}
		}
		if err := dumpMetadata(ctx, collection, spec, sink); err != nil {
			return err
		}
	}
	return nil
}

func dumpCollection(ctx context.Context, collection *mongo.Collection, sink FileWriter) error {
	name := dumpFileName(collection.Database().Name(), collection.Name(), ".bson")
	return sink.WriteFile(name, func(w io.Writer) error {
		cursor, err := collection.Find(ctx, bson.D{})
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		defer func() { _ = cursor.Close(ctx) }()
		for cursor.Next(ctx) {
			if _, err := w.Write(cursor.Current); err != nil {
				return fmt.Errorf("write %s: %w", name, err)
			}
		}
		if err := cursor.Err(); err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		return nil
	})
}

func dumpMetadata(ctx context.Context, collection *mongo.Collection, spec mongo.CollectionSpecification, sink FileWriter) error {
	name := dumpFileName(collection.Database().Name(), collection.Name(), ".metadata.json")
	indexes := bson.A{}
	if spec.Type != viewType {
		cursor, err := collection.Indexes().List(ctx)
		if err != nil {
			return fmt.Errorf("list indexes of %s: %w", name, err)
		}
		var raw []bson.Raw
		if err := cursor.All(ctx, &raw); err != nil {
			return fmt.Errorf("read indexes of %s: %w", name, err)
		}
		for _, index := range raw {
			indexes = append(indexes, index)
		}
	}
	options := spec.Options
	if options == nil {
		options = bson.Raw(emptyDocument)
	}
	metadata, err := bson.MarshalExtJSON(bson.D{
		{Key: "options", Value: options},
		{Key: "indexes", Value: indexes},
		{Key: "collectionName", Value: spec.Name},
		{Key: "type", Value: spec.Type},
	}, true, false)
	if err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}
	return sink.WriteFile(name, func(w io.Writer) error {
		_, err := w.Write(metadata)
		return err
	})
}

// emptyDocument is the BSON encoding of {}.
var emptyDocument = []byte{5, 0, 0, 0, 0}

// dumpFileName escapes the database and collection the way mongodump does,
// so a name holding a slash stays one path element.
func dumpFileName(database, collection, suffix string) string {
	return url.PathEscape(database) + "/" + url.PathEscape(collection) + suffix
}
