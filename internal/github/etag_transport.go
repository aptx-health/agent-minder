package github

import (
	"bytes"
	"io"
	"net/http"
	"sync"
)

// etagEntry holds a cached response body and its ETag for conditional requests.
type etagEntry struct {
	etag       string
	body       []byte
	statusCode int
	header     http.Header
}

// etagTransport is an http.RoundTripper that caches GET responses by ETag.
// When a cached ETag exists for a URL, it sends If-None-Match on repeat requests.
// On 304 Not Modified, it replays the cached response body — making the response
// transparent to callers while avoiding GitHub API rate limit charges.
type etagTransport struct {
	base  http.RoundTripper
	mu    sync.Mutex
	cache map[string]*etagEntry
}

// newETagTransport wraps a base transport with ETag caching.
func newETagTransport(base http.RoundTripper) *etagTransport {
	return &etagTransport{
		base:  base,
		cache: make(map[string]*etagEntry),
	}
}

func (t *etagTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Only cache GET requests.
	if req.Method != http.MethodGet {
		return t.base.RoundTrip(req)
	}

	key := req.URL.String()

	// If we have a cached ETag for this URL, send If-None-Match.
	t.mu.Lock()
	entry, ok := t.cache[key]
	t.mu.Unlock()

	if ok && entry.etag != "" {
		req = req.Clone(req.Context())
		req.Header.Set("If-None-Match", entry.etag)
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	// On 304, replay the cached response.
	if resp.StatusCode == http.StatusNotModified && ok {
		_ = resp.Body.Close()
		resp.StatusCode = entry.statusCode
		resp.Body = io.NopCloser(bytes.NewReader(entry.body))
		// Merge cached headers (Content-Type etc.) that 304 responses strip.
		for k, v := range entry.header {
			if resp.Header.Get(k) == "" {
				resp.Header[k] = v
			}
		}
		return resp, nil
	}

	// Cache the response if it has an ETag.
	etag := resp.Header.Get("ETag")
	if etag != "" {
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		// Replace the body so callers can still read it.
		resp.Body = io.NopCloser(bytes.NewReader(body))

		// Clone relevant headers for replay.
		hdrs := make(http.Header)
		for _, k := range []string{"Content-Type", "Content-Length"} {
			if v := resp.Header.Get(k); v != "" {
				hdrs.Set(k, v)
			}
		}

		t.mu.Lock()
		t.cache[key] = &etagEntry{
			etag:       etag,
			body:       body,
			statusCode: resp.StatusCode,
			header:     hdrs,
		}
		t.mu.Unlock()
	}

	return resp, nil
}
