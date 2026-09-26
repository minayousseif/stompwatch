package web

import (
	"sync"
	"time"
)

const (
	// maxCachedClips is how many resampled clips are kept in memory.
	maxCachedClips = 32
	// maxCachedBytes caps the same cache by size as well as by count. A
	// forced event can run for five minutes, which is 16 KiB a second once
	// the rate is raised, so a count on its own is not a bound on memory.
	maxCachedBytes = 64 << 20
)

// clipCache keeps resampled clips, the least recently used one dropped
// first. Raising the rate of a five minute clip is work worth doing once.
//
// The cache never holds more than a few dozen entries, so the oldest is
// found by looking at them all rather than by keeping a list in order.
type clipCache struct {
	mu      sync.Mutex
	entries map[int64]*cachedClip
	bytes   int
	clock   uint64 // counts uses, so the smallest used is the oldest
}

type cachedClip struct {
	mod  time.Time
	used uint64
	body []byte
}

func newClipCache() *clipCache {
	return &clipCache{entries: make(map[int64]*cachedClip)}
}

// get returns the clip of an event, but only if the file has not been
// written since it was cached.
func (c *clipCache) get(id int64, mod time.Time) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[id]
	if !ok {
		return nil, false
	}
	if !entry.mod.Equal(mod) {
		c.bytes -= len(entry.body)
		delete(c.entries, id)
		return nil, false
	}
	c.clock++
	entry.used = c.clock
	return entry.body, true
}

// put stores the clip of an event and drops the least recently used entries
// until the cache is inside both of its caps.
func (c *clipCache) put(id int64, mod time.Time, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[id]; ok {
		c.bytes -= len(old.body)
	}
	c.clock++
	c.entries[id] = &cachedClip{mod: mod, used: c.clock, body: body}
	c.bytes += len(body)
	for len(c.entries) > maxCachedClips || (c.bytes > maxCachedBytes && len(c.entries) > 1) {
		c.evictOldest()
	}
}

// evictOldest drops the entry that was used longest ago. The caller holds
// the lock.
func (c *clipCache) evictOldest() {
	var oldestID int64
	var oldest uint64
	first := true
	for id, entry := range c.entries {
		if first || entry.used < oldest {
			oldestID, oldest, first = id, entry.used, false
		}
	}
	if first {
		return
	}
	c.bytes -= len(c.entries[oldestID].body)
	delete(c.entries, oldestID)
}
