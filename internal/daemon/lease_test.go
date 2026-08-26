package daemon

import (
	"testing"
	"time"
)

// fastForward sets the admin clock to a fixed time.
func fastForward(a *Admin, t time.Time) {
	a.mu.Lock()
	a.now = func() time.Time { return t }
	a.mu.Unlock()
}

func shutdownSignaled(a *Admin) bool {
	select {
	case <-a.ShutdownCh():
		return true
	default:
		return false
	}
}

func TestGuestLeaseExpiresWithoutTouch(t *testing.T) {
	a := NewAdmin(1, "127.0.0.1:8338")
	start := time.Now()
	fastForward(a, start)

	if got := a.applyRefCountDelta(1); got != 2 {
		t.Fatalf("after +1 refcount = %d, want 2", got)
	}
	// No /mcp traffic: past the TTL the guest lease is reaped.
	fastForward(a, start.Add(DefaultLeaseTTL+time.Minute))
	if got := a.expireGuests(); got != 1 {
		t.Fatalf("after expiry refcount = %d, want 1 (owner only)", got)
	}
	if shutdownSignaled(a) {
		t.Fatal("owner lease must not trigger shutdown")
	}
}

func TestGuestLeaseSurvivesTouch(t *testing.T) {
	a := NewAdmin(1, "127.0.0.1:8338")
	start := time.Now()
	fastForward(a, start)

	a.applyRefCountDelta(1)
	// /mcp traffic at +5min refreshes the guest expiry to +20min.
	fastForward(a, start.Add(5*time.Minute))
	a.TouchGuests()
	// Past the original TTL but before the refreshed one: guest stays.
	fastForward(a, start.Add(DefaultLeaseTTL+time.Minute))
	if got := a.expireGuests(); got != 2 {
		t.Fatalf("after touch refcount = %d, want 2", got)
	}
	// Without further traffic it eventually expires.
	fastForward(a, start.Add(2*DefaultLeaseTTL+2*time.Minute))
	if got := a.expireGuests(); got != 1 {
		t.Fatalf("after second expiry refcount = %d, want 1", got)
	}
}

func TestRefCountDeltaGuestFirst(t *testing.T) {
	a := NewAdmin(1, "127.0.0.1:8338")
	start := time.Now()
	fastForward(a, start)

	a.applyRefCountDelta(1) // guest
	if got := a.applyRefCountDelta(-1); got != 1 {
		t.Fatalf("stop with guest: refcount = %d, want 1", got)
	}
	if shutdownSignaled(a) {
		t.Fatal("stop with a guest must not shut down the daemon")
	}
	// No guests left: the next -1 hits the owner and shuts down.
	if got := a.applyRefCountDelta(-1); got != 0 {
		t.Fatalf("stop owner: refcount = %d, want 0", got)
	}
	if !shutdownSignaled(a) {
		t.Fatal("stop owner must request shutdown")
	}
}

func TestExpiredGuestsShutdownDaemon(t *testing.T) {
	a := NewAdmin(1, "127.0.0.1:8338")
	start := time.Now()
	fastForward(a, start)

	a.applyRefCountDelta(1) // guest
	fastForward(a, start.Add(DefaultLeaseTTL+time.Minute))
	if got := a.expireGuests(); got != 1 {
		t.Fatalf("refcount = %d, want 1", got)
	}
	if shutdownSignaled(a) {
		t.Fatal("owner must survive guest expiry")
	}

	// A second guest that expires leaves only the owner; the daemon keeps
	// running until an explicit stop hits the owner.
	a.applyRefCountDelta(1)
	fastForward(a, start.Add(2*DefaultLeaseTTL+2*time.Minute))
	if got := a.expireGuests(); got != 1 {
		t.Fatalf("refcount = %d, want 1 (owner only)", got)
	}
	if shutdownSignaled(a) {
		t.Fatal("owner must survive guest expiry")
	}
	if got := a.applyRefCountDelta(-1); got != 0 {
		t.Fatalf("stop owner: refcount = %d, want 0", got)
	}
	if !shutdownSignaled(a) {
		t.Fatal("count reaching zero must request shutdown")
	}
}
