package imageproxy

import (
	"container/list"
	"sync"
	"time"
)

const entryOverhead = 128 // bookkeeping bytes charged per cache entry

type cacheEntry struct {
	key     string
	res     *Result
	err     *Error
	size    int64
	expires time.Time
}

// lru is a size-bounded, TTL-aware LRU cache of fetch outcomes (images and
// failures), keyed by URL.
type lru struct {
	mu   sync.Mutex
	max  int64
	used int64
	ll   *list.List // front = most recently used
	m    map[string]*list.Element
	now  func() time.Time
}

func newLRU(max int64, now func() time.Time) *lru {
	return &lru{max: max, ll: list.New(), m: make(map[string]*list.Element), now: now}
}

func (c *lru) get(key string) (*Result, *Error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[key]
	if !ok {
		return nil, nil, false
	}
	e := el.Value.(*cacheEntry)
	if !c.now().Before(e.expires) {
		c.removeLocked(el)
		return nil, nil, false
	}
	c.ll.MoveToFront(el)
	return e.res, e.err, true
}

func (c *lru) put(key string, res *Result, err *Error, ttl time.Duration) {
	size := int64(len(key)) + entryOverhead
	if res != nil {
		size += int64(len(res.Data))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.removeLocked(el)
	}
	if size > c.max {
		return
	}
	for c.used+size > c.max {
		c.removeLocked(c.ll.Back())
	}
	e := &cacheEntry{key: key, res: res, err: err, size: size, expires: c.now().Add(ttl)}
	c.m[key] = c.ll.PushFront(e)
	c.used += size
}

func (c *lru) removeLocked(el *list.Element) {
	e := el.Value.(*cacheEntry)
	c.ll.Remove(el)
	delete(c.m, e.key)
	c.used -= e.size
}

// sweep drops expired entries.
func (c *lru) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for el := c.ll.Back(); el != nil; {
		prev := el.Prev()
		if !now.Before(el.Value.(*cacheEntry).expires) {
			c.removeLocked(el)
		}
		el = prev
	}
}

func (c *lru) stats() (entries int, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len(), c.used
}
