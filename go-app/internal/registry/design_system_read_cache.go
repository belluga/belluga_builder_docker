package registry

import (
	"encoding/json"
	"sync"

	ac "belluga-builder-viewer/artifact_catalog"
)

const (
	designSystemReadCacheEntries     = 2
	designSystemReadCacheMetadataMax = 4 << 20
	designSystemReadCacheEntryMax    = ac.MaxTotalBytes + designSystemReadCacheMetadataMax
	designSystemReadCacheTotalMax    = designSystemReadCacheEntries * designSystemReadCacheEntryMax
)

type cachedDesignSystemSource struct {
	evaluation ac.DesignSystemResponse
	assets     map[string][]byte
	size       int64
}

// designSystemReadCache holds only immutable exact-revision evaluator results
// and their registered asset bytes. Its fixed entry and per-entry byte limits
// keep memory bounded; keys are scoped to every evaluator identity dimension.
type designSystemReadCache struct {
	mu      sync.Mutex
	entries map[string]cachedDesignSystemSource
	order   []string
	total   int64
}

func newDesignSystemReadCache() *designSystemReadCache {
	return &designSystemReadCache{entries: make(map[string]cachedDesignSystemSource)}
}

func designSystemReadCacheKey(projectID, companyID, repositoryID, revision, level string) string {
	return companyID + "\x00" + projectID + "\x00" + repositoryID + "\x00" + revision + "\x00" + level
}

func (c *designSystemReadCache) get(key string) (cachedDesignSystemSource, bool) {
	if c == nil {
		return cachedDesignSystemSource{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return cachedDesignSystemSource{}, false
	}
	for index, candidate := range c.order {
		if candidate == key {
			c.order = append(c.order[:index], c.order[index+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
	return entry, true
}

func (c *designSystemReadCache) put(key string, evaluation ac.DesignSystemResponse, assets map[string][]byte) {
	if c == nil {
		return
	}
	evaluationBytes, err := json.Marshal(evaluation)
	if err != nil || len(evaluationBytes) > designSystemReadCacheMetadataMax {
		return
	}
	entry := cachedDesignSystemSource{evaluation: evaluation, assets: make(map[string][]byte), size: int64(len(evaluationBytes))}
	assetBytes := int64(0)
	for path, data := range assets {
		if int64(len(data)) > ac.MaxTotalBytes-assetBytes {
			return
		}
		entry.assets[path] = append([]byte(nil), data...)
		entry.size += int64(len(data))
		assetBytes += int64(len(data))
	}
	if entry.size > designSystemReadCacheEntryMax {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; exists {
		c.total -= c.entries[key].size
		delete(c.entries, key)
		for index, candidate := range c.order {
			if candidate == key {
				c.order = append(c.order[:index], c.order[index+1:]...)
				break
			}
		}
	}
	for len(c.order) > 0 && (len(c.order) >= designSystemReadCacheEntries || c.total+entry.size > designSystemReadCacheTotalMax) {
		oldest := c.order[0]
		c.total -= c.entries[oldest].size
		delete(c.entries, oldest)
		c.order = c.order[1:]
	}
	c.entries[key] = entry
	c.total += entry.size
	c.order = append(c.order, key)
}
