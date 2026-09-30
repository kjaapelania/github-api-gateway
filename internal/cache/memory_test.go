package cache_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github-api-gateway/internal/cache"
)

func TestMemoryCache_GetMissAndCacheOn200(t *testing.T) {
	c := cache.New(5 * time.Minute)

	var upstreamCalls int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		w.Header().Set("ETag", `"etag-123"`)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name": "v1.0.0"}`))
	})

	handler := c.Middleware(upstream)

	req := httptest.NewRequest("GET", "/repos/my-org/repo/releases/latest", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got: %d", rec.Code)
	}
	if body := rec.Body.String(); body != `{"tag_name": "v1.0.0"}` {
		t.Errorf("unexpected body: %s", body)
	}
	if rec.Header().Get("ETag") != `"etag-123"` {
		t.Errorf("expected ETag header in response")
	}
	if c.Count() != 1 {
		t.Errorf("expected cache count 1, got: %d", c.Count())
	}
	if atomic.LoadInt32(&upstreamCalls) != 1 {
		t.Errorf("expected 1 upstream call, got: %d", upstreamCalls)
	}
}

func TestMemoryCache_RevalidationAnd304Hit(t *testing.T) {
	c := cache.New(5 * time.Minute)

	var lastIfNoneMatch string
	var upstreamCalls int32

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls := atomic.AddInt32(&upstreamCalls, 1)
		lastIfNoneMatch = r.Header.Get("If-None-Match")

		if calls == 1 {
			// First call: 200 OK with ETag
			w.Header().Set("ETag", `"etag-abc"`)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-RateLimit-Remaining", "4999")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"version": "1.0.0"}`))
			return
		}

		// Subsequent call: verify If-None-Match was sent
		if r.Header.Get("If-None-Match") == `"etag-abc"` {
			w.Header().Set("X-RateLimit-Remaining", "4999") // 0 quota consumed
			w.Header().Set("X-Custom-Header", "present-on-304")
			w.WriteHeader(http.StatusNotModified)
			return
		}

		w.WriteHeader(http.StatusBadRequest)
	})

	handler := c.Middleware(upstream)

	// Call 1: Miss & Cache
	req1 := httptest.NewRequest("GET", "/repos/my-org/repo/releases", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("first call: expected 200, got: %d", rec1.Code)
	}

	// Call 2: Hit with 304 upstream -> Relay serves 200 OK + cached body
	req2 := httptest.NewRequest("GET", "/repos/my-org/repo/releases", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if lastIfNoneMatch != `"etag-abc"` {
		t.Errorf("expected upstream to receive If-None-Match: \"etag-abc\", got: %s", lastIfNoneMatch)
	}

	// Relay must translate 304 to 200 with the cached body
	if rec2.Code != http.StatusOK {
		t.Errorf("expected relay to serve 200 OK on 304 revalidation, got: %d", rec2.Code)
	}
	if body := rec2.Body.String(); body != `{"version": "1.0.0"}` {
		t.Errorf("expected cached body, got: %s", body)
	}
	if rem := rec2.Header().Get("X-RateLimit-Remaining"); rem != "4999" {
		t.Errorf("expected rate limit remaining 4999, got: %s", rem)
	}
	if custom := rec2.Header().Get("X-Custom-Header"); custom != "present-on-304" {
		t.Errorf("expected updated headers from 304 to be propagated, got: %s", custom)
	}
	if atomic.LoadInt32(&upstreamCalls) != 2 {
		t.Errorf("expected 2 upstream calls, got: %d", upstreamCalls)
	}
}

func TestMemoryCache_Revalidation200Update(t *testing.T) {
	c := cache.New(5 * time.Minute)

	var upstreamCalls int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls := atomic.AddInt32(&upstreamCalls, 1)
		if calls == 1 {
			w.Header().Set("ETag", `"etag-v1"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`v1`))
			return
		}
		// Server resource changed -> returns new 200 OK with new ETag
		w.Header().Set("ETag", `"etag-v2"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`v2`))
	})

	handler := c.Middleware(upstream)

	// Call 1
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, httptest.NewRequest("GET", "/test", nil))
	if rec1.Body.String() != "v1" {
		t.Fatalf("call 1: unexpected body %s", rec1.Body.String())
	}

	// Call 2
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest("GET", "/test", nil))
	if rec2.Body.String() != "v2" {
		t.Fatalf("call 2: expected updated body v2, got %s", rec2.Body.String())
	}
	if rec2.Header().Get("ETag") != `"etag-v2"` {
		t.Fatalf("call 2: expected updated ETag v2, got %s", rec2.Header().Get("ETag"))
	}
}

func TestMemoryCache_302RedirectPassthrough(t *testing.T) {
	c := cache.New(5 * time.Minute)

	s3URL := "https://objects.githubusercontent.com/github-production-release-asset-2e65be/12345/app.tar.gz"
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GitHub release binary asset download returns 302 Found to S3
		http.Redirect(w, r, s3URL, http.StatusFound)
	})

	handler := c.Middleware(upstream)

	req := httptest.NewRequest("GET", "/repos/my-org/repo/releases/assets/999", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 Found, got: %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != s3URL {
		t.Errorf("expected Location %s, got: %s", s3URL, loc)
	}
	if c.Count() != 0 {
		t.Errorf("expected 302 responses NOT to be cached, cache count: %d", c.Count())
	}
}

func TestMemoryCache_NonGetRequestsNotCached(t *testing.T) {
	c := cache.New(5 * time.Minute)

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"post-etag"`)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"created": true}`))
	})

	handler := c.Middleware(upstream)

	req := httptest.NewRequest("POST", "/repos/my-org/repo/issues", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got: %d", rec.Code)
	}
	if c.Count() != 0 {
		t.Errorf("expected POST requests NOT to be cached, got count: %d", c.Count())
	}
}

func TestMemoryCache_TTLExpiration(t *testing.T) {
	c := cache.New(30 * time.Millisecond)

	var lastIfNoneMatch string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastIfNoneMatch = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"expiring-etag"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`content`))
	})

	handler := c.Middleware(upstream)

	// Call 1: Cache it
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ttl-test", nil))
	if c.Count() != 1 {
		t.Fatalf("expected cache count 1, got %d", c.Count())
	}

	// Sleep past TTL
	time.Sleep(50 * time.Millisecond)

	// Call 2: TTL expired, should not send If-None-Match
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ttl-test", nil))
	if lastIfNoneMatch != "" {
		t.Errorf("expected no If-None-Match after TTL expiration, got: %s", lastIfNoneMatch)
	}
}
