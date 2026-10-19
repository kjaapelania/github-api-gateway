package policy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Fetcher handles retrieving policy YAML documents from either local files or remote HTTP/HTTPS URLs.
type Fetcher struct {
	client *http.Client
}

// NewFetcher constructs a Fetcher with a default 10-second timeout if no client is supplied.
func NewFetcher(client *http.Client) *Fetcher {
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
		}
	}
	return &Fetcher{client: client}
}

// Fetch loads the policy content from the given source.
// Returns the raw byte slice, a boolean indicating if the content changed, and any error encountered.
func (f *Fetcher) Fetch(ctx context.Context, src *PolicySource) ([]byte, bool, error) {
	if src == nil {
		return nil, false, fmt.Errorf("policy source is nil")
	}

	if !src.IsRemote {
		return f.fetchLocal(src)
	}
	return f.fetchRemote(ctx, src)
}

func (f *Fetcher) fetchLocal(src *PolicySource) ([]byte, bool, error) {
	data, err := os.ReadFile(src.URI)
	if err != nil {
		return nil, false, fmt.Errorf("failed to read policy file %s: %w", src.URI, err)
	}

	hash := sha256.Sum256(data)
	src.mu.Lock()
	defer src.mu.Unlock()
	if src.LastHash == hash && len(src.RawContent) > 0 {
		return nil, false, nil
	}

	src.LastHash = hash
	src.LastModified = time.Now()
	src.RawContent = data
	return data, true, nil
}

func (f *Fetcher) fetchRemote(ctx context.Context, src *PolicySource) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URI, nil)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create remote policy request: %w", err)
	}

	src.mu.RLock()
	lastETag := src.LastETag
	src.mu.RUnlock()

	if lastETag != "" {
		req.Header.Set("If-None-Match", lastETag)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("remote policy fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, false, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("remote policy HTTP error %d: %s", resp.StatusCode, resp.Status)
	}

	// Limit to MaxPolicySizeBytes + 1 to detect overflow
	limitReader := io.LimitReader(resp.Body, int64(MaxPolicySizeBytes)+1)
	data, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, false, fmt.Errorf("failed reading remote policy body: %w", err)
	}

	if len(data) > MaxPolicySizeBytes {
		return nil, false, fmt.Errorf("remote policy payload (%d bytes) exceeds maximum allowed size of %d bytes", len(data), MaxPolicySizeBytes)
	}

	hash := sha256.Sum256(data)
	src.mu.Lock()
	defer src.mu.Unlock()
	if src.LastHash == hash && len(src.RawContent) > 0 {
		return nil, false, nil
	}

	etag := resp.Header.Get("ETag")
	src.LastETag = etag
	src.LastHash = hash
	src.LastModified = time.Now()
	src.RawContent = data

	return data, true, nil
}
