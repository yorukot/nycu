package oauth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// expirySet remembers IDs until they expire. It backs single-use codes and
// rotated refresh tokens, and assumes one server replica.
type expirySet struct {
	mu    sync.Mutex
	items map[string]time.Time
}

func newExpirySet() *expirySet { return &expirySet{items: map[string]time.Time{}} }

// add records id until expiry and reports whether it was new.
func (s *expirySet) add(id string, expiry, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, at := range s.items {
		if !now.Before(at) {
			delete(s.items, key)
		}
	}
	if at, seen := s.items[id]; seen && now.Before(at) {
		return false
	}
	s.items[id] = expiry
	return true
}

const (
	failureWindow     = 15 * time.Minute
	maxFailuresPerIP  = 5
	maxFailuresGlobal = 50
)

// failureLimiter slows password guessing on the authorization page: an address
// is locked out after 5 wrong passwords in 15 minutes, and every address after
// 50, which bounds a distributed guess as well.
type failureLimiter struct {
	mu     sync.Mutex
	byIP   map[string][]time.Time
	global []time.Time
}

func newFailureLimiter() *failureLimiter { return &failureLimiter{byIP: map[string][]time.Time{}} }

func recent(times []time.Time, now time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if now.Sub(t) < failureWindow {
			kept = append(kept, t)
		}
	}
	return kept
}

func (l *failureLimiter) blocked(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.global = recent(l.global, now)
	l.byIP[ip] = recent(l.byIP[ip], now)
	if len(l.byIP[ip]) == 0 {
		delete(l.byIP, ip)
	}
	return len(l.byIP[ip]) >= maxFailuresPerIP || len(l.global) >= maxFailuresGlobal
}

func (l *failureLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byIP[ip] = append(l.byIP[ip], now)
	l.global = append(l.global, now)
}

// clientIP is the caller's address. Behind Dokploy's Traefik the last
// X-Forwarded-For entry is the one the proxy appended, which a client cannot forge.
func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
