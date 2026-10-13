// Package api is the HTTP layer. The contract lives in openapi/openapi.yaml; a test fails if the
// routes registered here drift from it.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/auth"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/metrics"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/ratelimit"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/store"
)

// Options are the server's dependencies.
type Options struct {
	Auth              auth.Introspector
	Store             store.Store
	Pipeline          *resolver.Pipeline
	Limiter           *ratelimit.Limiter
	Metrics           *metrics.Metrics
	Logger            *slog.Logger
	ReqLog            *RequestLogger
	HashKey           []byte
	DefaultRatePerMin int
	MaxBodyBytes      int64
	DataVersion       string
	Now               func() time.Time
}

// Server implements http.Handler.
type Server struct {
	o      Options
	mux    *http.ServeMux
	routes []string

	tenantMu    sync.Mutex
	tenantCache map[string]cachedTenant
}

type cachedTenant struct {
	s  store.TenantSettings
	at time.Time
}

const tenantTTL = time.Minute

// reqState is the per-request record that handlers fill in and the outer layer reads when
// the request is over (for the log and the metrics).
type reqState struct {
	ID         string
	Start      time.Time
	Endpoint   string
	Principal  *auth.Principal
	Tenant     store.TenantSettings
	RatePerMin int
	Status     int
	ErrorCode  string

	Precision string
	Conf      *float64
	StateID   int
	LGA       string
	Sources   []string
	Warnings  []string
	InputHash []byte
	Raw       *store.RawText
}

type stateKey struct{}

type handlerFunc func(w http.ResponseWriter, r *http.Request, st *reqState)

// New builds the server and registers every route.
func New(o Options) *Server {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DefaultRatePerMin < 1 {
		o.DefaultRatePerMin = 60
	}
	if o.MaxBodyBytes < 1 {
		o.MaxBodyBytes = 128 << 10
	}
	if o.DataVersion == "" {
		o.DataVersion = "dev"
	}
	s := &Server{o: o, mux: http.NewServeMux(), tenantCache: map[string]cachedTenant{}}

	s.route("POST /v1/normalize", "normalize", "normalize", s.handleNormalize)
	s.route("POST /v1/geocode", "geocode", "geocode", s.handleGeocode)
	s.route("POST /v1/geocode:batch", "geocode_batch", "geocode", s.handleBatch)
	s.route("POST /v1/corrections", "corrections", "corrections:write", s.handleCorrection)
	s.open("GET /healthz", "healthz", s.handleHealth)
	s.open("GET /readyz", "readyz", s.handleReady)
	s.mux.HandleFunc("/", s.handleUnknown)
	return s
}

// Routes lists the registered "METHOD /path" patterns (used by the contract-drift test).
func (s *Server) Routes() []string {
	out := append([]string(nil), s.routes...)
	sort.Strings(out)
	return out
}

func (s *Server) route(pattern, endpoint, scope string, h handlerFunc) {
	s.routes = append(s.routes, pattern)
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		st := stateOf(r)
		st.Endpoint = endpoint
		if !s.authenticate(w, r, st, scope) {
			return
		}
		h(w, r, st)
	})
}

func (s *Server) open(pattern, endpoint string, h handlerFunc) {
	s.routes = append(s.routes, pattern)
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		st := stateOf(r)
		st.Endpoint = endpoint
		h(w, r, st)
	})
}

func stateOf(r *http.Request) *reqState {
	st, _ := r.Context().Value(stateKey{}).(*reqState)
	if st == nil {
		st = &reqState{} // only in tests that call a handler directly
	}
	return st
}

// ServeHTTP wraps every request: id, body limit, panic recovery, then logging and metrics.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	st := &reqState{ID: newUUID(), Start: s.o.Now(), Endpoint: "other"}
	w.Header().Set("X-Request-ID", st.ID)
	rw := &statusWriter{ResponseWriter: w}
	r = r.WithContext(context.WithValue(r.Context(), stateKey{}, st))
	r.Body = http.MaxBytesReader(rw, r.Body, s.o.MaxBodyBytes)

	defer func() {
		if rec := recover(); rec != nil {
			s.o.Logger.Error("panic in handler", "request_id", st.ID, "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			if !rw.wrote {
				fail(rw, st, newProblem(http.StatusInternalServerError, "internal_error", "Internal error", "Something failed on our side."))
			}
			st.ErrorCode = "internal_error"
		}
		s.finish(st, rw.statusCode())
	}()
	s.mux.ServeHTTP(rw, r)
}

func (s *Server) finish(st *reqState, status int) {
	st.Status = status
	dur := s.o.Now().Sub(st.Start)
	class := "none"
	if st.Principal != nil {
		class = st.Tenant.TrafficClass
		if class == "" {
			class = "real"
		}
	}
	s.o.Metrics.ObserveRequest(st.Endpoint, status, class, dur)

	attrs := []any{"request_id", st.ID, "endpoint", st.Endpoint, "status", status,
		"latency_ms", dur.Milliseconds(), "traffic_class", class}
	if st.Principal != nil {
		attrs = append(attrs, "tenant_id", st.Principal.TenantID)
	}
	if st.ErrorCode != "" {
		attrs = append(attrs, "error_code", st.ErrorCode)
	}
	s.o.Logger.Info("request", attrs...) // never the address, never the key

	if st.Principal != nil && s.o.ReqLog != nil {
		rec := store.RequestRecord{ID: st.ID, ReceivedAt: st.Start.UTC(), TenantID: st.Principal.TenantID,
			KeyID: st.Principal.KeyID, Endpoint: st.Endpoint, TrafficClass: class, Status: status,
			LatencyMS: int(dur.Milliseconds()), InputHash: st.InputHash, Precision: st.Precision,
			Confidence: st.Conf, StateID: st.StateID, LGAName: st.LGA, SourcesUsed: st.Sources,
			Warnings: st.Warnings, ErrorCode: st.ErrorCode}
		s.o.ReqLog.Log(logItem{req: &rec, raw: st.Raw})
	}
}

// ---- authentication, tenants, rate limiting

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, st *reqState, scope string) bool {
	key := r.Header.Get("X-API-Key")
	if key == "" {
		fail(w, st, newProblem(http.StatusUnauthorized, "unauthorized", "Unauthorized", "Send your API key in the X-API-Key header."))
		return false
	}
	p, err := s.o.Auth.Introspect(r.Context(), key)
	switch {
	case errors.Is(err, auth.ErrInvalidKey):
		fail(w, st, newProblem(http.StatusUnauthorized, "unauthorized", "Unauthorized", "The API key is not valid."))
		return false
	case err != nil:
		w.Header().Set("Retry-After", "5")
		fail(w, st, newProblem(http.StatusServiceUnavailable, "auth_unavailable", "Service unavailable", "Authentication is temporarily unavailable. Retry shortly."))
		return false
	}
	st.Principal = &p
	if !p.HasScope(scope) {
		fail(w, st, newProblem(http.StatusForbidden, "insufficient_scope", "Forbidden", "This key lacks the "+scope+" scope."))
		return false
	}

	st.Tenant = s.tenantSettings(r.Context(), p)
	st.RatePerMin = s.o.DefaultRatePerMin
	if p.RatePerMin > 0 {
		st.RatePerMin = p.RatePerMin
	}
	if st.Tenant.RatePerMin != nil {
		st.RatePerMin = *st.Tenant.RatePerMin // an operator override wins
	}
	return s.charge(w, st, 1)
}

// tenantSettings creates the tenant on first sight and caches its settings for a minute, so an
// operator's change (retain_raw, rate limit, traffic class) takes effect within that minute.
// If the store is down the request is still served with safe defaults.
func (s *Server) tenantSettings(ctx context.Context, p auth.Principal) store.TenantSettings {
	now := s.o.Now()
	s.tenantMu.Lock()
	c, ok := s.tenantCache[p.TenantID]
	s.tenantMu.Unlock()
	if ok && now.Sub(c.at) < tenantTTL {
		return c.s
	}
	ts, err := s.o.Store.EnsureTenant(ctx, p.TenantID, p.TenantName)
	if err != nil {
		s.o.Logger.Warn("tenant lookup failed; using defaults", "err", err.Error())
		if ok {
			return c.s
		}
		return store.TenantSettings{TrafficClass: "real"}
	}
	s.tenantMu.Lock()
	s.tenantCache[p.TenantID] = cachedTenant{s: ts, at: now}
	s.tenantMu.Unlock()
	return ts
}

// charge takes tokens from the tenant's bucket, sets the RateLimit headers, and writes a 429 when empty.
func (s *Server) charge(w http.ResponseWriter, st *reqState, cost int) bool {
	d := s.o.Limiter.Allow(st.Principal.TenantID, st.RatePerMin, cost)
	h := w.Header()
	h.Set("RateLimit-Limit", strconv.Itoa(d.Limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(d.Remaining))
	h.Set("RateLimit-Reset", strconv.Itoa(d.ResetSeconds))
	if d.Allowed {
		return true
	}
	h.Set("Retry-After", strconv.Itoa(d.RetryAfterSeconds))
	p := newProblem(http.StatusTooManyRequests, "rate_limited", "Too many requests",
		"Your tenant's rate limit is used up. Retry after the number of seconds in Retry-After.")
	p.RetryAfter = d.RetryAfterSeconds
	fail(w, st, p)
	return false
}

// ---- small helpers

func (s *Server) inputHash(c resolver.Components) []byte {
	m := hmac.New(sha256.New, s.o.HashKey)
	m.Write([]byte(c.Canonical()))
	return m.Sum(nil)
}

func (s *Server) stateID(c resolver.Components) int {
	if c.State == "" {
		return 0
	}
	if si, ok := s.o.Pipeline.Norm.StateByName(c.State); ok {
		return si.ID
	}
	return 0
}

func (s *Server) recordAnswer(st *reqState, a resolver.Answer) {
	st.Precision = string(resolver.None)
	st.Conf = nil
	if a.Fix != nil {
		st.Precision = string(a.Fix.Precision)
		c := a.Fix.Confidence
		st.Conf = &c
	}
	st.StateID = s.stateID(a.Normalized)
	st.LGA = a.Normalized.LGA
	st.Sources = a.SourcesUsed
	st.Warnings = a.Warnings
	st.InputHash = s.inputHash(a.Normalized)
	class := st.Tenant.TrafficClass
	if class == "" {
		class = "real"
	}
	s.o.Metrics.ObserveResult(st.Precision, class)
	for _, n := range a.SourcesUsed {
		s.o.Metrics.ObserveSource(n, "ok")
	}
	for _, n := range a.SourcesFailed {
		s.o.Metrics.ObserveSource(n, "error")
	}
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
