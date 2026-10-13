package benthos

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// Cached values are bounded independently of the inline label limit, which does
// not limit the full message retained by an execution result.
const maxExecutionCacheBytes = 16 << 20
const maxExecutionCacheEntryBytes = 1 << 20

type executionKey struct {
	uri    string
	digest [32]byte
}
type executionEntry struct {
	key     executionKey
	result  *PartialResult
	err     error
	created time.Time
	bytes   int
}
type executionFlight struct {
	done chan struct{}
}
type executionCache struct {
	mu      sync.Mutex
	entries map[executionKey]*list.Element
	pending map[executionKey]*executionFlight
	lru     list.List
	bytes   int
}

func cloneResult(result *PartialResult) *PartialResult {
	if result == nil {
		return nil
	}
	out := *result
	out.Value = cloneValue(result.Value)
	if result.Meta != nil {
		out.Meta = cloneValue(result.Meta).(map[string]any)
	}
	return &out
}

func (e *Executor) cachedExecution(ctx context.Context, uri string, sample *Sample, snippet string, execute func() (*PartialResult, error)) (*PartialResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.config.PartialExecCacheSize <= 0 || e.config.PartialExecCacheTTL <= 0 {
		return execute()
	}
	// Hash actual sample contents, so callers may change fixtures without relying
	// on pointer identity. Effective snippets share before/after statement results.
	fixture, err := json.Marshal(sample)
	if err != nil {
		return execute()
	}
	hash := sha256.New()
	_, _ = hash.Write(fixture)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(snippet))
	key := executionKey{uri: uri, digest: [32]byte(hash.Sum(nil))}
	c := &e.cache
	for {
		c.mu.Lock()
		if c.entries == nil {
			c.entries = make(map[executionKey]*list.Element)
			c.pending = make(map[executionKey]*executionFlight)
		}
		if element := c.entries[key]; element != nil {
			entry := element.Value.(*executionEntry)
			if time.Since(entry.created) < e.config.PartialExecCacheTTL {
				c.lru.MoveToFront(element)
				result, cachedErr := entry.result, entry.err
				c.mu.Unlock()
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				return cloneResult(result), cachedErr
			}
			c.remove(element)
		}
		if pending := c.pending[key]; pending != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-pending.done:
				continue
			}
		}
		flight := &executionFlight{done: make(chan struct{})}
		c.pending[key] = flight
		c.mu.Unlock()
		result, execErr := execute()
		if ctx.Err() != nil {
			result, execErr = nil, ctx.Err()
		}
		size := len(uri) + len(snippet)
		if result != nil {
			size += len(result.Full) + len(result.Text)
			metadata, err := json.Marshal(result.Meta)
			if err != nil {
				size = maxExecutionCacheEntryBytes + 1
			} else {
				size += len(metadata)
			}
		}
		if execErr != nil {
			size += len(execErr.Error())
		}
		c.mu.Lock()
		currentFlight := c.pending[key] == flight
		if currentFlight {
			delete(c.pending, key)
		}
		// Invalidating while a function is running must not resurrect its result.
		if currentFlight && ctx.Err() == nil && !errors.Is(execErr, context.Canceled) && !errors.Is(execErr, context.DeadlineExceeded) && size <= maxExecutionCacheEntryBytes {
			entry := &executionEntry{key: key, result: cloneResult(result), err: execErr, created: time.Now(), bytes: size}
			c.entries[key] = c.lru.PushFront(entry)
			c.bytes += size
			for c.lru.Len() > e.config.PartialExecCacheSize || c.bytes > maxExecutionCacheBytes {
				c.remove(c.lru.Back())
			}
		}
		close(flight.done)
		c.mu.Unlock()
		return result, execErr
	}
}

func (c *executionCache) remove(element *list.Element) {
	entry := element.Value.(*executionEntry)
	delete(c.entries, entry.key)
	c.bytes -= entry.bytes
	c.lru.Remove(element)
}

func (e *Executor) InvalidateDocument(uri string) {
	c := &e.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, element := range c.entries {
		if DocumentKeyMatches(key.uri, uri) {
			c.remove(element)
		}
	}
	for key := range c.pending {
		if DocumentKeyMatches(key.uri, uri) {
			delete(c.pending, key)
		}
	}
}
