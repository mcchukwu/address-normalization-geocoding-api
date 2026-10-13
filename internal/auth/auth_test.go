package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type fake struct {
	calls atomic.Int32
	p     Principal
	err   error
}

func (f *fake) Introspect(context.Context, string) (Principal, error) {
	f.calls.Add(1)
	return f.p, f.err
}

func newCached(f *fake, clock *time.Time) *Cached {
	return &Cached{Inner: f, TTL: time.Minute, NegativeTTL: 10 * time.Second, Grace: 5 * time.Minute,
		Now: func() time.Time { return *clock }}
}

func TestCachedServesFromCacheUntilTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fake{p: Principal{TenantID: "t1"}}
	c := newCached(f, &now)
	for i := 0; i < 5; i++ {
		if _, err := c.Introspect(context.Background(), "k"); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls.Load() != 1 {
		t.Errorf("want 1 upstream call within the TTL, got %d", f.calls.Load())
	}
	now = now.Add(61 * time.Second)
	c.Introspect(context.Background(), "k")
	if f.calls.Load() != 2 {
		t.Errorf("want a refresh after the TTL, got %d calls", f.calls.Load())
	}
}

func TestRevokedKeyStopsWorkingWithinTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fake{p: Principal{TenantID: "t1"}}
	c := newCached(f, &now)
	c.Introspect(context.Background(), "k")
	f.err = ErrInvalidKey // revoked upstream
	now = now.Add(61 * time.Second)
	if _, err := c.Introspect(context.Background(), "k"); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("want ErrInvalidKey after revocation, got %v", err)
	}
}

func TestInvalidKeysAreNegativelyCached(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fake{err: ErrInvalidKey}
	c := newCached(f, &now)
	for i := 0; i < 5; i++ {
		c.Introspect(context.Background(), "guess")
	}
	if f.calls.Load() != 1 {
		t.Errorf("a guessing flood should cost one upstream call, got %d", f.calls.Load())
	}
}

func TestStaleEntryServedWhenAuthServiceIsDown(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fake{p: Principal{TenantID: "t1"}}
	c := newCached(f, &now)
	c.Introspect(context.Background(), "k")
	f.err = ErrUnavailable
	now = now.Add(3 * time.Minute) // past TTL, inside TTL+Grace
	if p, err := c.Introspect(context.Background(), "k"); err != nil || p.TenantID != "t1" {
		t.Errorf("want the stale principal during an outage, got %v %v", p, err)
	}
	now = now.Add(10 * time.Minute) // beyond grace
	if _, err := c.Introspect(context.Background(), "k"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("want ErrUnavailable once the grace is used up, got %v", err)
	}
	if _, err := c.Introspect(context.Background(), "never-seen"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("an unknown key during an outage must fail closed, got %v", err)
	}
}

func TestHTTPIntrospector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		switch in["api_key"] {
		case "good":
			json.NewEncoder(w).Encode(map[string]any{"active": true, "tenant_id": "t1", "tenant_name": "Acme",
				"key_id": "k1", "scopes": []string{"geocode"}, "rate_limit_per_min": 120})
		case "revoked":
			json.NewEncoder(w).Encode(map[string]any{"active": false})
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	h := HTTPIntrospector{URL: srv.URL}

	p, err := h.Introspect(context.Background(), "good")
	if err != nil || p.TenantID != "t1" || p.RatePerMin != 120 || !p.HasScope("geocode") || p.HasScope("admin") {
		t.Errorf("good key: %+v %v", p, err)
	}
	if _, err := h.Introspect(context.Background(), "revoked"); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("revoked key: %v", err)
	}
	if _, err := h.Introspect(context.Background(), "boom"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("upstream 500 should be unavailable, got %v", err)
	}
	srv.Close()
	if _, err := h.Introspect(context.Background(), "good"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unreachable service should be unavailable, got %v", err)
	}
}

func TestStaticParse(t *testing.T) {
	s, err := ParseStatic("devkey|11111111-1111-1111-1111-111111111111|Dev|geocode,normalize;other|22222222-2222-2222-2222-222222222222|Two|geocode")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Introspect(context.Background(), "devkey")
	if err != nil || p.TenantName != "Dev" || !p.HasScope("normalize") {
		t.Errorf("%+v %v", p, err)
	}
	if _, err := s.Introspect(context.Background(), "nope"); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("want ErrInvalidKey, got %v", err)
	}
	if _, err := ParseStatic("broken"); err == nil {
		t.Errorf("malformed spec should fail")
	}
}
