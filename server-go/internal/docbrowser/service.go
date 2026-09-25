package docbrowser

import (
	"context"
	"encoding/json"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	DefaultLimit     = 20
	MaxLimit         = 100
	SampleSize       = 10
	defaultTimeout   = 15 * time.Second
	defaultSampleTTL = 30 * time.Second
)

// Options tune the service; zero values take the defaults.
type Options struct {
	Timeout   time.Duration
	SampleTTL time.Duration
	Now       func() time.Time
}

// FindRequest is a find as the API receives it: each document part is
// Extended JSON text, empty meaning none.
type FindRequest struct {
	Filter     string
	Sort       string
	Projection string
	Limit      int64
	Skip       int64
}

// Page is one page of documents, each as canonical Extended JSON.
type Page struct {
	Documents []json.RawMessage `json:"documents"`
	Limit     int64             `json:"limit"`
	Skip      int64             `json:"skip"`
}

// Service validates every request before it reaches the gateway and bounds
// each one with a deadline.
type Service struct {
	connector Connector
	timeout   time.Duration
	samples   *sampleCache
}

func NewService(connector Connector, o Options) *Service {
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	if o.SampleTTL <= 0 {
		o.SampleTTL = defaultSampleTTL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{connector: connector, timeout: o.Timeout, samples: newSampleCache(o.SampleTTL, o.Now)}
}

// run opens the project's store and calls fn under the request deadline.
func (s *Service) run(ctx context.Context, projectID string, fn func(context.Context, Store) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	store, err := s.connector.Store(ctx, projectID)
	if err != nil {
		return err
	}
	return classify(fn(ctx, store))
}

func (s *Service) ListDatabases(ctx context.Context, projectID string) ([]string, error) {
	names := []string{}
	err := s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		all, err := store.ListDatabases(ctx)
		for _, name := range all {
			if !systemDatabases[name] {
				names = append(names, name)
			}
		}
		return err
	})
	return names, err
}

func (s *Service) ListCollections(ctx context.Context, projectID, database string) ([]Collection, error) {
	if err := validateDatabase(database); err != nil {
		return nil, err
	}
	collections := []Collection{}
	err := s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		found, err := store.ListCollections(ctx, database)
		collections = append(collections, found...)
		return err
	})
	return collections, err
}

func (s *Service) CreateCollection(ctx context.Context, projectID string, ns Namespace) error {
	if err := validateNamespace(ns); err != nil {
		return err
	}
	return s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		return store.CreateCollection(ctx, ns)
	})
}

func (s *Service) DropCollection(ctx context.Context, projectID string, ns Namespace) error {
	if err := validateNamespace(ns); err != nil {
		return err
	}
	defer s.samples.forget(sampleKey{project: projectID, ns: ns})
	return s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		return store.DropCollection(ctx, ns)
	})
}

func parseFind(req FindRequest) (FindQuery, error) {
	if req.Skip < 0 {
		return FindQuery{}, invalid("skip must not be negative")
	}
	query := FindQuery{Limit: req.Limit, Skip: req.Skip}
	if query.Limit <= 0 {
		query.Limit = DefaultLimit
	}
	query.Limit = min(query.Limit, MaxLimit)
	var err error
	if query.Filter, err = parseDocument("filter", []byte(req.Filter), true); err != nil {
		return FindQuery{}, err
	}
	if query.Sort, err = parseDocument("sort", []byte(req.Sort), true); err != nil {
		return FindQuery{}, err
	}
	if query.Projection, err = parseDocument("projection", []byte(req.Projection), true); err != nil {
		return FindQuery{}, err
	}
	return query, nil
}

func (s *Service) Find(ctx context.Context, projectID string, ns Namespace, req FindRequest) (Page, error) {
	if err := validateNamespace(ns); err != nil {
		return Page{}, err
	}
	query, err := parseFind(req)
	if err != nil {
		return Page{}, err
	}
	var docs []json.RawMessage
	err = s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		found, err := store.Find(ctx, ns, query)
		if err != nil {
			return err
		}
		docs, err = rawsToExtJSON(found)
		return err
	})
	return Page{Documents: docs, Limit: query.Limit, Skip: query.Skip}, err
}

func rawsToExtJSON(raws []bson.Raw) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(raws))
	for _, raw := range raws {
		doc, err := toExtJSON(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, nil
}

func (s *Service) Count(ctx context.Context, projectID string, ns Namespace, filter string) (int64, error) {
	if err := validateNamespace(ns); err != nil {
		return 0, err
	}
	parsed, err := parseDocument("filter", []byte(filter), true)
	if err != nil {
		return 0, err
	}
	var count int64
	err = s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		count, err = store.Count(ctx, ns, parsed)
		return err
	})
	return count, err
}

func (s *Service) Insert(ctx context.Context, projectID string, ns Namespace, body []byte) (json.RawMessage, error) {
	if err := validateNamespace(ns); err != nil {
		return nil, err
	}
	doc, err := parseDocument("the document", body, false)
	if err != nil {
		return nil, err
	}
	defer s.samples.forget(sampleKey{project: projectID, ns: ns})
	var id json.RawMessage
	err = s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		inserted, err := store.InsertOne(ctx, ns, doc)
		if err != nil {
			return err
		}
		id, err = valueToExtJSON(inserted)
		return err
	})
	return id, err
}

func (s *Service) Replace(ctx context.Context, projectID string, ns Namespace, rawID string, body []byte) error {
	id, doc, err := parseTargeted(ns, rawID, body, "the document")
	if err != nil {
		return err
	}
	for _, field := range doc {
		if field.Key == "_id" && !sameValue(field.Value, id) {
			return invalid("a replacement cannot change the document's _id")
		}
	}
	return s.write(ctx, projectID, ns, func(ctx context.Context, store Store) (int64, error) {
		return store.ReplaceOne(ctx, ns, id, doc)
	})
}

func (s *Service) Update(ctx context.Context, projectID string, ns Namespace, rawID string, body []byte) error {
	id, update, err := parseTargeted(ns, rawID, body, "the update")
	if err != nil {
		return err
	}
	if !onlyOperators(update) {
		return invalid("an update must consist only of update operators such as $set")
	}
	return s.write(ctx, projectID, ns, func(ctx context.Context, store Store) (int64, error) {
		return store.UpdateOne(ctx, ns, id, update)
	})
}

func (s *Service) Delete(ctx context.Context, projectID string, ns Namespace, rawID string) error {
	if err := validateNamespace(ns); err != nil {
		return err
	}
	id, err := parseID(rawID)
	if err != nil {
		return err
	}
	return s.write(ctx, projectID, ns, func(ctx context.Context, store Store) (int64, error) {
		return store.DeleteOne(ctx, ns, id)
	})
}

func parseTargeted(ns Namespace, rawID string, body []byte, what string) (any, bson.D, error) {
	if err := validateNamespace(ns); err != nil {
		return nil, nil, err
	}
	id, err := parseID(rawID)
	if err != nil {
		return nil, nil, err
	}
	doc, err := parseDocument(what, body, false)
	return id, doc, err
}

// write runs a single-document write and reports a document that was not
// there as not found.
func (s *Service) write(ctx context.Context, projectID string, ns Namespace, fn func(context.Context, Store) (int64, error)) error {
	defer s.samples.forget(sampleKey{project: projectID, ns: ns})
	return s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		affected, err := fn(ctx, store)
		if err == nil && affected == 0 {
			return ErrDocumentNotFound
		}
		return err
	})
}

func (s *Service) ListIndexes(ctx context.Context, projectID string, ns Namespace) ([]json.RawMessage, error) {
	if err := validateNamespace(ns); err != nil {
		return nil, err
	}
	var indexes []json.RawMessage
	err := s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		found, err := store.ListIndexes(ctx, ns)
		if err != nil {
			return err
		}
		indexes, err = rawsToExtJSON(found)
		return err
	})
	return indexes, err
}

func (s *Service) CreateIndex(ctx context.Context, projectID string, ns Namespace, body []byte) (string, error) {
	if err := validateNamespace(ns); err != nil {
		return "", err
	}
	spec, err := parseIndexSpec(body)
	if err != nil {
		return "", err
	}
	var name string
	err = s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		name, err = store.CreateIndex(ctx, ns, spec)
		return err
	})
	return name, err
}

func (s *Service) DropIndex(ctx context.Context, projectID string, ns Namespace, name string) error {
	if err := validateNamespace(ns); err != nil {
		return err
	}
	if err := validateIndexName(name); err != nil {
		return err
	}
	return s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		return store.DropIndex(ctx, ns, name)
	})
}

// Sample returns up to SampleSize random documents, which Studio reads field
// paths from for query autocompletion.
func (s *Service) Sample(ctx context.Context, projectID string, ns Namespace) ([]json.RawMessage, error) {
	if err := validateNamespace(ns); err != nil {
		return nil, err
	}
	key := sampleKey{project: projectID, ns: ns}
	if docs, ok := s.samples.get(key); ok {
		return docs, nil
	}
	var docs []json.RawMessage
	err := s.run(ctx, projectID, func(ctx context.Context, store Store) error {
		found, err := store.Sample(ctx, ns, SampleSize)
		if err != nil {
			return err
		}
		docs, err = rawsToExtJSON(found)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.samples.put(key, docs)
	return docs, nil
}
