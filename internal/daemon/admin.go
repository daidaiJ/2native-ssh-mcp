package daemon

import (
	"encoding/json"
	"net"
	"net/http"
	"sync/atomic"
)

// Admin is the daemon admin API: health, status, refcount and shutdown,
// served under /__admin/ and restricted to loopback clients.
type Admin struct {
	refCount    atomic.Int32
	shutdownCh  chan struct{}
	shutdownOnce atomic.Bool
}

// NewAdmin creates an admin API with the given initial refcount.
func NewAdmin(initialRefCount int32) *Admin {
	a := &Admin{shutdownCh: make(chan struct{}, 1)}
	a.refCount.Store(initialRefCount)
	return a
}

// RefCount returns the current refcount.
func (a *Admin) RefCount() int32 { return a.refCount.Load() }

// ShutdownCh is signaled when the daemon should exit.
func (a *Admin) ShutdownCh() <-chan struct{} { return a.shutdownCh }

// Handler returns the admin mux.
func (a *Admin) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/__admin/refcount", a.localOnly(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Delta int `json:"delta"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		newVal := a.refCount.Add(int32(req.Delta))
		if newVal < 0 {
			a.refCount.Store(0)
			newVal = 0
		}
		resp := RefCountResponse{Name: ServerName, RefCount: int(newVal)}
		if newVal == 0 {
			resp.Message = "refcount reached zero, server will shutdown gracefully"
			a.requestShutdown()
		}
		json.NewEncoder(w).Encode(resp)
	}))
	mux.HandleFunc("/__admin/status", a.localOnly(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(RefCountResponse{Name: ServerName, RefCount: int(a.refCount.Load())})
	}))
	mux.HandleFunc("/__admin/shutdown", a.localOnly(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message":"shutdown requested"}`))
		a.requestShutdown()
	}))
	mux.HandleFunc("/__admin/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(RefCountResponse{Name: ServerName, RefCount: int(a.refCount.Load())})
	})
	return mux
}

func (a *Admin) requestShutdown() {
	if a.shutdownOnce.CompareAndSwap(false, true) {
		select {
		case a.shutdownCh <- struct{}{}:
		default:
		}
	}
}

// localOnly rejects requests that do not come from a loopback address.
func (a *Admin) localOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}