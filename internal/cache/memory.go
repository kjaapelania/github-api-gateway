package cache

import (
	"bytes"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Entry represents a cached HTTP response.
type Entry struct {
	ETag       string
	StatusCode int
	Header     http.Header
	Body       []byte
	CachedAt   time.Time
}

// DefaultMaxEntries is the default maximum number of cached ETag responses stored in memory.
const DefaultMaxEntries = 10000

// Cache provides a thread-safe in-memory ETag response cache for 0-quota revalidations.
type Cache struct {
	mu         sync.RWMutex
	entries    map[string]*Entry
	ttl        time.Duration
	maxEntries int
}

// New creates a new in-memory response cache with the specified TTL.
func New(ttl time.Duration) *Cache {
	return &Cache{
		entries:    make(map[string]*Entry),
		ttl:        ttl,
		maxEntries: DefaultMaxEntries,
	}
}

// SetMaxEntries sets the maximum number of cache entries retained in memory.
func (c *Cache) SetMaxEntries(max int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if max <= 0 {
		max = DefaultMaxEntries
	}
	c.maxEntries = max
}

// Get retrieves an entry if it exists and has not expired.
func (c *Cache) Get(key string) (*Entry, bool) {
	c.mu.RLock()
	entry, found := c.entries[key]
	if !found {
		c.mu.RUnlock()
		return nil, false
	}
	if c.ttl > 0 && time.Since(entry.CachedAt) > c.ttl {
		c.mu.RUnlock()
		c.mu.Lock()
		if e, ok := c.entries[key]; ok && time.Since(e.CachedAt) > c.ttl {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return nil, false
	}
	c.mu.RUnlock()
	return entry, true
}

// Set stores or updates an entry in the cache, evicting expired or oldest entries if at capacity.
func (c *Cache) Set(key string, entry *Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[key]; !exists && c.maxEntries > 0 && len(c.entries) >= c.maxEntries {
		c.evictLocked(time.Now())
	}
	c.entries[key] = entry
}

func (c *Cache) evictLocked(now time.Time) {
	// First sweep any expired entries
	if c.ttl > 0 {
		for k, e := range c.entries {
			if now.Sub(e.CachedAt) > c.ttl {
				delete(c.entries, k)
			}
		}
	}
	if len(c.entries) < c.maxEntries {
		return
	}
	// Otherwise evict the oldest entry
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, e := range c.entries {
		if first || e.CachedAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = e.CachedAt
			first = false
		}
	}
	if !first {
		delete(c.entries, oldestKey)
	}
}

// Count returns the number of active, unexpired entries in the cache.
func (c *Cache) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	count := 0
	for _, entry := range c.entries {
		if c.ttl <= 0 || now.Sub(entry.CachedAt) <= c.ttl {
			count++
		}
	}
	return count
}

// Size is an alias for Count.
func (c *Cache) Size() int {
	return c.Count()
}

// Clear flushes all entries from the cache.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*Entry)
}

// Middleware returns an HTTP middleware that intercepts GET requests, sends If-None-Match
// revalidations, caches 200 OK responses with ETags, and transforms upstream 304 Not Modified
// into 200 OK with cached bodies.
func (c *Cache) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only GET requests participate in ETag caching
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}

		key := r.URL.RequestURI()
		if key == "" {
			key = r.URL.Path
		}

		entry, found := c.Get(key)
		if found && entry != nil && entry.ETag != "" {
			r.Header.Set("If-None-Match", entry.ETag)
		}

		cw := newCaptureWriter(w)
		next.ServeHTTP(cw, r)

		if cw.passthrough {
			return
		}

		// Handle 304 Not Modified from upstream
		if cw.statusCode == http.StatusNotModified && found && entry != nil {
			// First copy cached headers
			for k, vv := range entry.Header {
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			// Overlay fresh headers from the 304 response (updated rate limits, dates, etc.)
			for k, vv := range cw.header {
				w.Header().Del(k)
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			// Ensure Content-Length reflects cached body
			w.Header().Set("Content-Length", strconv.Itoa(len(entry.Body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(entry.Body)

			// Refresh cache entry timestamp
			c.mu.Lock()
			entry.CachedAt = time.Now()
			c.mu.Unlock()
			return
		}

		// Handle 200 OK from upstream
		if cw.statusCode == http.StatusOK {
			for k, vv := range cw.header {
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(http.StatusOK)
			bodyBytes := cw.body.Bytes()
			_, _ = w.Write(bodyBytes)

			if etag := cw.header.Get("ETag"); etag != "" {
				c.Set(key, &Entry{
					ETag:       etag,
					StatusCode: http.StatusOK,
					Header:     cw.header.Clone(),
					Body:       bodyBytes,
					CachedAt:   time.Now(),
				})
			}
			return
		}

		// Handle any other status code
		for k, vv := range cw.header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		if cw.statusCode > 0 {
			w.WriteHeader(cw.statusCode)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write(cw.body.Bytes())
	})
}

type captureWriter struct {
	underlying  http.ResponseWriter
	header      http.Header
	statusCode  int
	body        *bytes.Buffer
	wroteHeader bool
	passthrough bool
}

func newCaptureWriter(w http.ResponseWriter) *captureWriter {
	return &captureWriter{
		underlying: w,
		header:     make(http.Header),
		body:       new(bytes.Buffer),
	}
}

func (cw *captureWriter) Header() http.Header {
	if cw.passthrough {
		return cw.underlying.Header()
	}
	return cw.header
}

func (cw *captureWriter) WriteHeader(statusCode int) {
	if cw.wroteHeader {
		return
	}
	cw.wroteHeader = true
	cw.statusCode = statusCode

	// Non-200 and non-304 responses are streamed immediately to client without buffering
	if statusCode != http.StatusOK && statusCode != http.StatusNotModified {
		cw.passthrough = true
		for k, vv := range cw.header {
			for _, v := range vv {
				cw.underlying.Header().Add(k, v)
			}
		}
		cw.underlying.WriteHeader(statusCode)
	}
}

func (cw *captureWriter) Write(b []byte) (int, error) {
	if !cw.wroteHeader {
		cw.WriteHeader(http.StatusOK)
	}
	if cw.passthrough {
		return cw.underlying.Write(b)
	}
	return cw.body.Write(b)
}
