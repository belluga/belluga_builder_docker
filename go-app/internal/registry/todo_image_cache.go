package registry

import (
	"context"
	"sync"
	"time"
)

const todoImageCacheTTL = 30 * time.Second
const todoImageFailureTTL = 2 * time.Second
const todoImageCacheEntries = 16
const todoImageGitConcurrency = 2
const todoImageGitWaiters = 32

type todoImageCacheEntry struct {
	file    todoImageFile
	err     error
	expires time.Time
}

// The TODO is checked before this cache is consulted. Only Git-backed bytes
// are cached, with a short TTL and bounded concurrent misses.
type todoImageCache struct {
	mu      sync.Mutex
	entries map[string]todoImageCacheEntry
	loading map[string]chan struct{}
	slots   chan struct{}
	waiters chan struct{}
}

func (c *todoImageCache) load(ctx context.Context, key string, fetch func() (todoImageFile, error)) (todoImageFile, error) {
	for {
		c.mu.Lock()
		if entry, ok := c.entries[key]; ok {
			if time.Now().Before(entry.expires) {
				c.mu.Unlock()
				return entry.file, entry.err
			}
			delete(c.entries, key)
		}
		if pending := c.loading[key]; pending != nil {
			select {
			case c.waiters <- struct{}{}:
			default:
				c.mu.Unlock()
				return todoImageFile{}, ErrUnavailable
			}
			c.mu.Unlock()
			select {
			case <-pending:
				<-c.waiters
				continue
			case <-ctx.Done():
				<-c.waiters
				return todoImageFile{}, ErrUnavailable
			}
		}
		if c.slots == nil {
			c.slots = make(chan struct{}, todoImageGitConcurrency)
			c.waiters = make(chan struct{}, todoImageGitWaiters)
		}
		select {
		case c.waiters <- struct{}{}:
		default:
			c.mu.Unlock()
			return todoImageFile{}, ErrUnavailable
		}
		if c.loading == nil {
			c.loading = make(map[string]chan struct{})
		}
		pending := make(chan struct{})
		c.loading[key] = pending
		c.mu.Unlock()
		select {
		case c.slots <- struct{}{}:
		case <-ctx.Done():
			c.mu.Lock()
			delete(c.loading, key)
			close(pending)
			<-c.waiters
			c.mu.Unlock()
			return todoImageFile{}, ErrUnavailable
		}

		file, err := fetch()
		c.mu.Lock()
		if c.entries == nil {
			c.entries = make(map[string]todoImageCacheEntry)
		}
		if len(c.entries) >= todoImageCacheEntries {
			var oldestKey string
			var oldest time.Time
			for candidate, entry := range c.entries {
				if oldestKey == "" || entry.expires.Before(oldest) {
					oldestKey, oldest = candidate, entry.expires
				}
			}
			delete(c.entries, oldestKey)
		}
		ttl := todoImageCacheTTL
		if err != nil {
			ttl = todoImageFailureTTL
		}
		c.entries[key] = todoImageCacheEntry{file: file, err: err, expires: time.Now().Add(ttl)}
		delete(c.loading, key)
		close(pending)
		<-c.slots
		<-c.waiters
		c.mu.Unlock()
		return file, err
	}
}
