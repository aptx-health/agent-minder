package github

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestETagTransport_CachesAndReplays304(t *testing.T) {
	var hits atomic.Int32
	body := `{"id":1,"title":"test issue"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("If-None-Match") == `"abc123"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	transport := newETagTransport(http.DefaultTransport)
	client := &http.Client{Transport: transport}

	// First request — should get full response and cache it.
	resp, err := client.Get(srv.URL + "/repos/o/r/issues/1")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(got) != body {
		t.Fatalf("first request body = %q, want %q", got, body)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("first request status = %d, want 200", resp.StatusCode)
	}
	if hits.Load() != 1 {
		t.Fatalf("server hits = %d, want 1", hits.Load())
	}

	// Second request — should send If-None-Match, get 304, replay cached body.
	resp2, err := client.Get(srv.URL + "/repos/o/r/issues/1")
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if string(got2) != body {
		t.Fatalf("second request body = %q, want %q", got2, body)
	}
	if resp2.StatusCode != 200 {
		t.Fatalf("second request status = %d, want 200 (replayed)", resp2.StatusCode)
	}
	if hits.Load() != 2 {
		t.Fatalf("server hits = %d, want 2 (conditional request still hits server)", hits.Load())
	}
}

func TestETagTransport_SkipsNonGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"should-not-cache"`)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	transport := newETagTransport(http.DefaultTransport)
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/repos/o/r/issues", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// Cache should be empty — POST shouldn't be cached.
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.cache) != 0 {
		t.Fatalf("cache has %d entries after POST, want 0", len(transport.cache))
	}
}

func TestETagTransport_UpdatesCacheOnNewETag(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		// Always return 200 with a new ETag (simulates changed resource).
		w.Header().Set("ETag", `"v`+string(rune('0'+n))+`"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":` + string(rune('0'+n)) + `}`))
	}))
	defer srv.Close()

	transport := newETagTransport(http.DefaultTransport)
	client := &http.Client{Transport: transport}

	// First call.
	resp, _ := client.Get(srv.URL + "/test")
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// Second call — server returns new data (not 304), cache should update.
	resp2, _ := client.Get(srv.URL + "/test")
	got, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()

	transport.mu.Lock()
	entry := transport.cache[srv.URL+"/test"]
	transport.mu.Unlock()

	if entry == nil {
		t.Fatal("expected cache entry")
	}
	if string(got) != string(entry.body) {
		t.Fatalf("response body %q doesn't match cached body %q", got, entry.body)
	}
}

func TestETagTransport_NoETagNoCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`no etag`))
	}))
	defer srv.Close()

	transport := newETagTransport(http.DefaultTransport)
	client := &http.Client{Transport: transport}

	resp, _ := client.Get(srv.URL + "/test")
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.cache) != 0 {
		t.Fatalf("cache has %d entries, want 0 (no ETag in response)", len(transport.cache))
	}
}
