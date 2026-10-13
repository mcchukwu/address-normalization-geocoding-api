// Package auth checks API keys. Keys, tenants and scopes belong to the auth service; this
// package asks it (introspection), caches the answer briefly, and survives short outages.
package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Principal is who a valid key belongs to.
type Principal struct {
	TenantID   string
	TenantName string
	KeyID      string
	Scopes     []string
	RatePerMin int // 0 means "use the service default"
}

// HasScope reports whether the key carries a scope.
func (p Principal) HasScope(s string) bool {
	for _, x := range p.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

var (
	// ErrInvalidKey means the key is unknown, revoked or expired.
	ErrInvalidKey = errors.New("invalid api key")
	// ErrUnavailable means the auth service could not be reached and nothing usable is cached.
	ErrUnavailable = errors.New("auth service unavailable")
)

// Introspector turns an API key into a Principal.
type Introspector interface {
	Introspect(ctx context.Context, key string) (Principal, error)
}

// HTTPIntrospector calls the auth service. The contract it expects (RFC 7662 in spirit):
//
//	POST {URL}   {"api_key": "..."}
//	200 {"active": true, "tenant_id": "...", "tenant_name": "...", "key_id": "...",
//	     "scopes": ["geocode"], "rate_limit_per_min": 120}
//	200 {"active": false}                      -> invalid key
type HTTPIntrospector struct {
	URL    string
	Client *http.Client
}

type introspectResponse struct {
	Active     bool     `json:"active"`
	TenantID   string   `json:"tenant_id"`
	TenantName string   `json:"tenant_name"`
	KeyID      string   `json:"key_id"`
	Scopes     []string `json:"scopes"`
	RatePerMin int      `json:"rate_limit_per_min"`
}

// Introspect implements Introspector.
func (h HTTPIntrospector) Introspect(ctx context.Context, key string) (Principal, error) {
	body, _ := json.Marshal(map[string]string{"api_key": key})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Principal{}, ErrInvalidKey
	case resp.StatusCode != http.StatusOK:
		return Principal{}, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	var out introspectResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Principal{}, fmt.Errorf("%w: bad response: %v", ErrUnavailable, err)
	}
	if !out.Active || out.TenantID == "" {
		return Principal{}, ErrInvalidKey
	}
	return Principal{TenantID: out.TenantID, TenantName: out.TenantName, KeyID: out.KeyID,
		Scopes: out.Scopes, RatePerMin: out.RatePerMin}, nil
}

// Static is a development introspector with keys fixed at start-up.
type Static struct {
	keys map[[32]byte]Principal
}

// ParseStatic reads "key|tenant_id|tenant_name|scope,scope;key2|..." into a Static.
func ParseStatic(spec string) (*Static, error) {
	s := &Static{keys: map[[32]byte]Principal{}}
	for i, entry := range strings.Split(spec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		f := strings.Split(entry, "|")
		if len(f) != 4 || f[0] == "" || f[1] == "" {
			return nil, fmt.Errorf("static key %d: want key|tenant_id|tenant_name|scopes", i+1)
		}
		s.keys[sha256.Sum256([]byte(f[0]))] = Principal{
			TenantID: f[1], TenantName: f[2], KeyID: fmt.Sprintf("static_%d", i+1),
			Scopes: strings.Split(f[3], ","),
		}
	}
	if len(s.keys) == 0 {
		return nil, errors.New("no static keys configured")
	}
	return s, nil
}

// Introspect implements Introspector.
func (s *Static) Introspect(_ context.Context, key string) (Principal, error) {
	sum := sha256.Sum256([]byte(key))
	for k, p := range s.keys { // constant-time compare across all keys
		if subtle.ConstantTimeCompare(k[:], sum[:]) == 1 {
			return p, nil
		}
	}
	return Principal{}, ErrInvalidKey
}

// Cached wraps an Introspector with a short cache so a revoked key stops working within
// TTL without a network call on every request.
//   - valid keys are remembered for TTL;
//   - invalid keys are remembered for NegativeTTL, which blunts guessing floods;
//   - if the auth service fails, an entry up to Grace past its TTL is still served.
//
// Keys are stored only as SHA-256 hashes.
type Cached struct {
	Inner       Introspector
	TTL         time.Duration
	NegativeTTL time.Duration
	Grace       time.Duration
	Now         func() time.Time

	mu sync.Mutex
	m  map[[32]byte]entry
}

type entry struct {
	p       Principal
	invalid bool
	at      time.Time
}

// Introspect implements Introspector.
func (c *Cached) Introspect(ctx context.Context, key string) (Principal, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	h := sha256.Sum256([]byte(key))

	c.mu.Lock()
	if c.m == nil {
		c.m = map[[32]byte]entry{}
	}
	e, ok := c.m[h]
	c.mu.Unlock()

	if ok {
		age := now().Sub(e.at)
		if e.invalid && age < c.NegativeTTL {
			return Principal{}, ErrInvalidKey
		}
		if !e.invalid && age < c.TTL {
			return e.p, nil
		}
	}

	p, err := c.Inner.Introspect(ctx, key)
	switch {
	case err == nil:
		c.store(h, entry{p: p, at: now()})
		return p, nil
	case errors.Is(err, ErrInvalidKey):
		c.store(h, entry{invalid: true, at: now()})
		return Principal{}, ErrInvalidKey
	default:
		if ok && !e.invalid && now().Sub(e.at) < c.TTL+c.Grace {
			return e.p, nil // stale-if-error
		}
		return Principal{}, ErrUnavailable
	}
}

func (c *Cached) store(h [32]byte, e entry) {
	c.mu.Lock()
	c.m[h] = e
	c.mu.Unlock()
}
