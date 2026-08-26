package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminLocalOnly(t *testing.T) {
	admin := NewAdmin(1, "127.0.0.1:8338")
	h := admin.Handler()

	cases := []struct {
		name       string
		remoteAddr string
		host       string
		want       int
	}{
		{"loopback remote + loopback host", "127.0.0.1:1", "127.0.0.1", http.StatusOK},
		{"loopback remote + host with port", "127.0.0.1:1", "127.0.0.1:8338", http.StatusOK},
		{"loopback remote + localhost host", "127.0.0.1:1", "localhost", http.StatusOK},
		{"loopback remote + ipv6 host", "127.0.0.1:1", "[::1]", http.StatusOK},
		{"loopback remote + evil host (DNS rebinding)", "127.0.0.1:1", "evil.test", http.StatusForbidden},
		{"loopback remote + evil host with port", "127.0.0.1:1", "evil.test:8338", http.StatusForbidden},
		{"non-loopback remote", "192.0.2.1:1234", "127.0.0.1", http.StatusForbidden},
		{"non-loopback remote + evil host", "192.0.2.1:1234", "evil.test", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/__admin/health", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Host = tc.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestAdminLocalOnlyListenHostAllowed(t *testing.T) {
	// Listening on a loopback address outside the default list: that host
	// is accepted in Host.
	admin := NewAdmin(1, "127.0.0.2:8338")
	h := admin.Handler()

	req := httptest.NewRequest(http.MethodGet, "/__admin/health", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Host = "127.0.0.2"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("listen host should be allowed, got %d", rec.Code)
	}

	// Non-loopback listen host is not added to the allowlist.
	admin = NewAdmin(1, "myhost.local:8338")
	h = admin.Handler()
	req = httptest.NewRequest(http.MethodGet, "/__admin/health", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Host = "myhost.local"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback listen host must not be allowed, got %d", rec.Code)
	}
}

func TestAdminLocalOnlyAllEndpoints(t *testing.T) {
	admin := NewAdmin(1, "127.0.0.1:8338")
	h := admin.Handler()

	for _, path := range []string{"/__admin/health", "/__admin/status", "/__admin/refcount", "/__admin/shutdown"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:1"
		req.Host = "evil.test"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s with evil Host: got %d, want 403", path, rec.Code)
		}
	}
}

func TestAdminShutdownRequiresPostJSON(t *testing.T) {
	admin := NewAdmin(1, "127.0.0.1:8338")
	h := admin.Handler()

	// A plain GET (e.g. <img src> from a local page) must not stop the daemon.
	req := httptest.NewRequest(http.MethodGet, "/__admin/shutdown", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET shutdown: got %d, want 405", rec.Code)
	}
	if shutdownSignaled(admin) {
		t.Fatal("GET shutdown must not signal shutdown")
	}

	// POST without the JSON content type is rejected too.
	req = httptest.NewRequest(http.MethodPost, "/__admin/shutdown", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Host = "127.0.0.1"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST shutdown without JSON: got %d, want 405", rec.Code)
	}
	if shutdownSignaled(admin) {
		t.Fatal("POST without JSON must not signal shutdown")
	}

	// The daemon client's request (POST + application/json) works.
	req = httptest.NewRequest(http.MethodPost, "/__admin/shutdown", nil)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1"
	req.Host = "127.0.0.1"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST shutdown with JSON: got %d, want 200", rec.Code)
	}
	if !shutdownSignaled(admin) {
		t.Fatal("POST shutdown with JSON must signal shutdown")
	}
}
