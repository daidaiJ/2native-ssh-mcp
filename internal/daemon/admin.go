package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultLeaseTTL is how long an extra `start` (+1) holds without any /mcp
// traffic. The daemon's own owner lease never expires.
const DefaultLeaseTTL = 15 * time.Minute

// leaseTickerInterval is how often expired guest leases are reaped.
const leaseTickerInterval = 30 * time.Second

// lease is a guest refcount entry; it expires after leaseTTL without /mcp
// traffic.
type lease struct {
	expires time.Time
}

// Admin is the daemon admin API: health, status, refcount and shutdown,
// served under /__admin/ and restricted to loopback clients.
type Admin struct {
	refCount     atomic.Int32
	shutdownCh   chan struct{}
	shutdownOnce atomic.Bool
	// allowedHosts are the Host header values accepted by localOnly, in
	// addition to the loopback defaults.
	allowedHosts []string
	// mu guards guests. The refcount itself is atomic: owner (1) plus the
	// number of live guest leases.
	mu       sync.Mutex
	guests   []lease
	leaseTTL time.Duration
	// now is the clock; tests override it to fast-forward past the TTL.
	now func() time.Time
}

// NewAdmin creates an admin API with the given initial refcount. listenAddr
// is the HTTP listen address; when it binds a loopback interface, its host
// is also accepted in the Host header.
func NewAdmin(initialRefCount int32, listenAddr string) *Admin {
	a := &Admin{
		shutdownCh: make(chan struct{}, 1),
		leaseTTL:   DefaultLeaseTTL,
		now:        time.Now,
	}
	a.refCount.Store(initialRefCount)
	if h, _, err := net.SplitHostPort(listenAddr); err == nil {
		listenAddr = h
	}
	listenAddr = strings.Trim(listenAddr, "[]")
	if ip := net.ParseIP(listenAddr); ip != nil && ip.IsLoopback() {
		a.allowedHosts = append(a.allowedHosts, strings.ToLower(listenAddr))
	}
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
		newVal := a.applyRefCountDelta(req.Delta)
		resp := RefCountResponse{Name: ServerName, RefCount: int(newVal)}
		if newVal == 0 {
			resp.Message = "refcount reached zero, server will shutdown gracefully"
		}
		json.NewEncoder(w).Encode(resp)
	}))
	mux.HandleFunc("/__admin/status", a.localOnly(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(RefCountResponse{Name: ServerName, RefCount: int(a.refCount.Load())})
	}))
	mux.HandleFunc("/__admin/shutdown", a.localOnly(func(w http.ResponseWriter, r *http.Request) {
		// POST + JSON only: a plain GET (e.g. <img src> from a local page)
		// must not be able to stop the daemon.
		if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Write([]byte(`{"message":"shutdown requested"}`))
		a.requestShutdown()
	}))
	mux.HandleFunc("/__admin/health", a.localOnly(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(RefCountResponse{Name: ServerName, RefCount: int(a.refCount.Load())})
	}))
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

// applyRefCountDelta applies a refcount change: +1 adds a guest lease with a
// TTL, -1 removes a guest lease first (falling back to the owner). The
// counter is settled under mu so a lease removal and its counter change are
// atomic: a concurrent expireGuests can never deduct the same lease twice.
// It returns the new refcount.
func (a *Admin) applyRefCountDelta(delta int) int32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if delta > 0 {
		expires := a.now().Add(a.leaseTTL)
		for i := 0; i < delta; i++ {
			a.guests = append(a.guests, lease{expires: expires})
		}
	} else if delta < 0 {
		remove := -delta
		for remove > 0 && len(a.guests) > 0 {
			a.guests = a.guests[:len(a.guests)-1]
			remove--
		}
		// The counter still moves by the full delta: leases that were not
		// removed fall back to the owner.
	}
	newVal := a.refCount.Add(int32(delta))
	if newVal < 0 {
		a.refCount.Store(0)
		newVal = 0
	}
	if newVal == 0 {
		a.requestShutdown()
	}
	return newVal
}

// TouchGuests refreshes the expiry of every guest lease. Called after each
// authorized /mcp request so an actively used daemon never drops guests.
func (a *Admin) TouchGuests() {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for i := range a.guests {
		a.guests[i].expires = now.Add(a.leaseTTL)
	}
}

// expireGuests drops guest leases past their expiry, decrementing the
// refcount accordingly under mu. When the count reaches zero the shutdown is
// requested. Returns the new refcount.
func (a *Admin) expireGuests() int32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	kept := a.guests[:0]
	expired := 0
	for _, g := range a.guests {
		if g.expires.Before(now) {
			expired++
			continue
		}
		kept = append(kept, g)
	}
	a.guests = kept
	if expired > 0 {
		newVal := a.refCount.Add(-int32(expired))
		if newVal <= 0 {
			a.refCount.Store(0)
			a.requestShutdown()
			return 0
		}
		return newVal
	}
	return a.refCount.Load()
}

// StartLeaseTicker reaps expired guest leases every leaseTickerInterval
// until ctx is done.
func (a *Admin) StartLeaseTicker(ctx context.Context) {
	ticker := time.NewTicker(leaseTickerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if a.expireGuests() == 0 {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// localOnly rejects requests that do not come from a loopback address with a
// loopback Host header. The Host check blocks DNS rebinding: a browser can
// reach 127.0.0.1 but its Host header would be the attacker's domain.
// X-Forwarded-* headers are never trusted.
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
		if !a.hostAllowed(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// hostAllowed reports whether the request Host header names a loopback host.
// Port and case are ignored; bracketed IPv6 is accepted.
func (a *Admin) hostAllowed(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	for _, allowed := range a.allowedHosts {
		if host == allowed {
			return true
		}
	}
	return false
}
