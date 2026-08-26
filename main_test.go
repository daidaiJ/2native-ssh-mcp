package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPortFromAddr(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		want    int
		wantErr bool
	}{
		{"host port", "127.0.0.1:8338", 8338, false},
		{"ipv6", "[::1]:9000", 9000, false},
		{"no port", "192.168.1.1", 0, true},
		{"not a port", "host:not-a-port", 0, true},
		{"empty", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := portFromAddr(tc.addr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("portFromAddr(%q) expected error, got %d", tc.addr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("portFromAddr(%q) unexpected error: %v", tc.addr, err)
			}
			if got != tc.want {
				t.Fatalf("portFromAddr(%q) = %d, want %d", tc.addr, got, tc.want)
			}
		})
	}
}

func TestIsLoopbackBind(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8338", true},
		{"127.0.0.1", true},
		{"localhost:8338", true},
		{"localhost", true},
		{"LOCALHOST:8338", true},
		{"[::1]:8338", true},
		{"::1", true},
		{"0.0.0.0:8338", false},
		{"0.0.0.0", false},
		{"[::]:8338", false},
		{"::", false},
		{":8338", false},
		{"192.168.1.10:8338", false},
		{"192.168.1.1", false},
		{"not-an-addr", false},
	}
	for _, tc := range cases {
		if got := isLoopbackBind(tc.addr); got != tc.want {
			t.Errorf("isLoopbackBind(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestRequireToken(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := requireToken(ok, "s3cret")

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct token: got %d, want 200", rec.Code)
	}
}
