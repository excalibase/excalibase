package docbrowser

import (
	"encoding/json"
	"sync"
	"time"
)

const maxCachedSamples = 256

type sampleKey struct {
	project string
	ns      Namespace
}

type sampleEntry struct {
	docs    []json.RawMessage
	expires time.Time
}

// sampleCache keeps each collection's field-inference sample for a short
// while, so typing in the query bar does not sample on every keystroke.
type sampleCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[sampleKey]sampleEntry
}

func newSampleCache(ttl time.Duration, now func() time.Time) *sampleCache {
	return &sampleCache{ttl: ttl, now: now, entries: map[sampleKey]sampleEntry{}}
}

func (c *sampleCache) get(key sampleKey) ([]json.RawMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !c.now().Before(entry.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.docs, true
}

func (c *sampleCache) put(key sampleKey, docs []json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.entries) >= maxCachedSamples {
		c.evict(now)
	}
	c.entries[key] = sampleEntry{docs: docs, expires: now.Add(c.ttl)}
}

// evict drops expired entries, and the one closest to expiry if that frees
// nothing.
func (c *sampleCache) evict(now time.Time) {
	var oldestKey sampleKey
	var oldest time.Time
	for key, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, key)
			continue
		}
		if oldest.IsZero() || entry.expires.Before(oldest) {
			oldestKey, oldest = key, entry.expires
		}
	}
	if len(c.entries) >= maxCachedSamples {
		delete(c.entries, oldestKey)
	}
}

func (c *sampleCache) forget(key sampleKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}
