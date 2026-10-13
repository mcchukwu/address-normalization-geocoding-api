package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/auth"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/metrics"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/ratelimit"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/store"
)

const (
	tenantA = "11111111-1111-4111-8111-111111111111"
	tenantB = "22222222-2222-4222-8222-222222222222"
)

type harness struct {
	t      *testing.T
	srv    *Server
	mem    *store.Memory
	reqlog *RequestLogger
	met    *metrics.Metrics
	auth   *flakyAuth
}

// flakyAuth wraps the static introspector so a test can simulate an auth-service outage.
type flakyAuth struct {
	inner auth.Introspector
	down  bool
}

func (f *flakyAuth) Introspect(ctx context.Context, key string) (auth.Principal, error) {
	if f.down {
		return auth.Principal{}, auth.ErrUnavailable
	}
	return f.inner.Introspect(ctx, key)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := auth.ParseStatic("keyA|" + tenantA + "|Tenant A|geocode,normalize,corrections:write;" +
		"keyB|" + tenantB + "|Tenant B|geocode,corrections:write;" +
		"readonly|" + tenantA + "|Tenant A|normalize")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, mem: store.NewMemory(), met: metrics.New(), auth: &flakyAuth{inner: st}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h.reqlog = NewRequestLogger(h.mem, 64, h.met, log)
	pipe := resolver.NewPipeline(resolver.NewNormalizer(resolver.DefaultTerms()), resolver.FixtureSource{})
	h.srv = New(Options{Auth: h.auth, Store: h.mem, Pipeline: pipe, Limiter: ratelimit.New(), Metrics: h.met,
		Logger: log, ReqLog: h.reqlog, HashKey: []byte("test-hash-key-0123456789"), DataVersion: "test-gaz-1"})
	return h
}

func (h *harness) do(method, path, key, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return rec
}

// flush waits for the async request log so tests can inspect what was written.
func (h *harness) flush() {
	h.reqlog.Close()
	h.reqlog = NewRequestLogger(h.mem, 64, h.met, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.srv.o.ReqLog = h.reqlog
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body.String())
	}
	return m
}

func expectProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) map[string]any {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status: got %d want %d\n%s", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("error content type: got %q", ct)
	}
	m := decodeBody(t, rec)
	if m["code"] != code {
		t.Errorf("code: got %v want %v", m["code"], code)
	}
	if m["request_id"] != rec.Header().Get("X-Request-ID") || m["request_id"] == "" {
		t.Errorf("problem request_id should match the X-Request-ID header: %v vs %q", m["request_id"], rec.Header().Get("X-Request-ID"))
	}
	return m
}

const landmarkAddr = `{"address":"opp. first bank, allen avenue, ikeja, lagos"}`

func TestGeocodeHappyPath(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/v1/geocode", "keyA", landmarkAddr)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	res := m["result"].(map[string]any)
	if res["precision"] != "street" || res["radius_m"] != float64(300) {
		t.Errorf("want a street fix with a 300 m radius: %v", res)
	}
	if m["data_version"] != "test-gaz-1" {
		t.Errorf("data_version: %v", m["data_version"])
	}
	norm := m["normalized"].(map[string]any)
	if norm["street"] != "allen avenue" || norm["state"] != "lagos" {
		t.Errorf("normalized: %v", norm)
	}
	for _, k := range []string{"candidates", "warnings"} {
		if _, ok := m[k].([]any); !ok {
			t.Errorf("%s must be an array, got %T", k, m[k])
		}
	}
	for _, hd := range []string{"X-Request-ID", "RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset"} {
		if rec.Header().Get(hd) == "" {
			t.Errorf("missing header %s", hd)
		}
	}
	if m["request_id"] != rec.Header().Get("X-Request-ID") {
		t.Errorf("request_id in the body must equal the header")
	}
}

func TestUnresolvableAddressIsOKWithNullResult(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/v1/geocode", "keyA", `{"address":"behind the mango tree"}`)
	if rec.Code != 200 {
		t.Fatalf("an unresolvable address is not an error: %d", rec.Code)
	}
	m := decodeBody(t, rec)
	if v, present := m["result"]; !present || v != nil {
		t.Errorf("result must be present and null, got %v (present=%v)", v, present)
	}
	if !contains(m["warnings"], "landmark_not_matched") {
		t.Errorf("warnings: %v", m["warnings"])
	}
}

func TestFirstRequestCreatesTenantAndLogsMetadataOnly(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/v1/geocode", "keyA", landmarkAddr)
	h.flush()
	ts, ok := h.mem.Tenants[tenantA]
	if !ok || ts.TrafficClass != "real" || ts.RetainRaw {
		t.Fatalf("tenant should exist with safe defaults: %+v ok=%v", ts, ok)
	}
	if len(h.mem.Requests) != 1 {
		t.Fatalf("want 1 logged request, got %d", len(h.mem.Requests))
	}
	r := h.mem.Requests[0]
	if r.TenantID != tenantA || r.Endpoint != "geocode" || r.Status != 200 || r.Precision != "street" ||
		r.StateID != 25 || len(r.InputHash) != 32 || r.KeyID == "" {
		t.Errorf("logged record: %+v", r)
	}
	if len(h.mem.RawTexts) != 0 {
		t.Errorf("raw text must not be kept without an opt-in")
	}
}

func TestRawTextOnlyForOptedInTenants(t *testing.T) {
	h := newHarness(t)
	h.mem.Tenants[tenantA] = store.TenantSettings{TrafficClass: "real", RetainRaw: true}
	h.do("POST", "/v1/geocode", "keyA", landmarkAddr)
	h.flush()
	if len(h.mem.RawTexts) != 1 {
		t.Fatalf("opted-in tenant should have raw text kept, got %d", len(h.mem.RawTexts))
	}
	rt := h.mem.RawTexts[0]
	if d := rt.ExpiresAt.Sub(rt.ReceivedAt); d != 30*24*time.Hour {
		t.Errorf("retention should be exactly 30 days, got %v", d)
	}
}

func TestAuthentication(t *testing.T) {
	h := newHarness(t)
	expectProblem(t, h.do("POST", "/v1/geocode", "", landmarkAddr), 401, "unauthorized")
	expectProblem(t, h.do("POST", "/v1/geocode", "wrong", landmarkAddr), 401, "unauthorized")
	// a valid key that lacks the scope
	m := expectProblem(t, h.do("POST", "/v1/geocode", "readonly", landmarkAddr), 403, "insufficient_scope")
	if !strings.Contains(m["detail"].(string), "geocode") {
		t.Errorf("detail should name the missing scope: %v", m["detail"])
	}
	// auth service outage fails closed with a retry hint
	h.auth.down = true
	rec := h.do("POST", "/v1/geocode", "keyA", landmarkAddr)
	expectProblem(t, rec, 503, "auth_unavailable")
	if rec.Header().Get("Retry-After") == "" {
		t.Errorf("503 should carry Retry-After")
	}
}

func TestValidation(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name, body string
		status     int
		code       string
		field      string
	}{
		{"too short", `{"address":"ab"}`, 422, "validation_failed", "address"},
		{"missing address", `{}`, 422, "validation_failed", "address"},
		{"unknown field", `{"address":"allen avenue","colour":"red"}`, 422, "validation_failed", "colour"},
		{"wrong type", `{"address":123}`, 422, "validation_failed", "address"},
		{"max_candidates too big", `{"address":"allen avenue","max_candidates":11}`, 422, "validation_failed", "max_candidates"},
		{"max_candidates zero", `{"address":"allen avenue","max_candidates":0}`, 422, "validation_failed", "max_candidates"},
		{"null hint", `{"address":"allen avenue","hints":{"lga":null,"state":"Lagos"}}`, 422, "validation_failed", "hints.lga"},
		{"null max_candidates", `{"address":"allen avenue","max_candidates":null}`, 422, "validation_failed", "max_candidates"},
		{"fractional max_candidates", `{"address":"allen avenue","max_candidates":2.5}`, 422, "validation_failed", "max_candidates"},
		{"blank address", `{"address":"     "}`, 422, "validation_failed", "address"},
		{"malformed json", `{"address":`, 400, "invalid_json", ""},
		{"empty body", ``, 400, "invalid_json", ""},
		{"trailing data", `{"address":"allen avenue"} {"x":1}`, 400, "invalid_json", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := expectProblem(t, h.do("POST", "/v1/geocode", "keyA", tc.body), tc.status, tc.code)
			if tc.field != "" {
				errs, _ := m["errors"].([]any)
				if len(errs) == 0 || errs[0].(map[string]any)["field"] != tc.field {
					t.Errorf("want an error on field %q, got %v", tc.field, m["errors"])
				}
			}
		})
	}
	long := `{"address":"` + strings.Repeat("a", 501) + `"}`
	expectProblem(t, h.do("POST", "/v1/geocode", "keyA", long), 422, "validation_failed")
	huge := `{"address":"` + strings.Repeat("a", 200<<10) + `"}`
	expectProblem(t, h.do("POST", "/v1/geocode", "keyA", huge), 413, "payload_too_large")
}

func TestIntegersMayBeWrittenWithAFractionalZero(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/v1/geocode", "keyA", `{"address":"allen avenue, ikeja","max_candidates":2.0}`)
	if rec.Code != 200 {
		t.Fatalf("6.0 is a valid integer under JSON Schema: %d %s", rec.Code, rec.Body)
	}
	if n := len(decodeBody(t, rec)["candidates"].([]any)); n > 2 {
		t.Errorf("max_candidates 2.0 should cap at 2, got %d", n)
	}
	// a short address padded with spaces is allowed by the contract (3+ characters, not blank)
	if rec := h.do("POST", "/v1/geocode", "keyA", `{"address":"a  "}`); rec.Code != 200 {
		t.Errorf("contract allows this: %d %s", rec.Code, rec.Body)
	}
}

// Found by schemathesis: a NUL character in a note made PostgreSQL refuse the insert, returning 500.
func TestNULCharactersNeverCauseAServerError(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/v1/geocode", "keyA", `{"address":"allen\u0000 avenue, ikeja","hints":{"lga":"ik\u0000eja"}}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	m := decodeBody(t, rec)
	if strings.ContainsRune(m["input"].(string), 0) || m["normalized"].(map[string]any)["street"] != "allen avenue" {
		t.Errorf("NUL should be stripped before normalising: %v", m)
	}
	reqID := m["request_id"].(string)
	h.flush()
	body := `{"request_id":"` + reqID + `","address":"allen avenue","confirmed":{"lat":6.6,"lng":3.35},"note":"a\u0000b"}`
	if rec := h.do("POST", "/v1/corrections", "keyA", body); rec.Code != 201 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if h.mem.Corrections[0].Note != "ab" {
		t.Errorf("note: %q", h.mem.Corrections[0].Note)
	}
}

func TestRateLimiting(t *testing.T) {
	h := newHarness(t)
	h.srv.o.DefaultRatePerMin = 3
	for i := 0; i < 3; i++ {
		if rec := h.do("POST", "/v1/normalize", "keyA", landmarkAddr); rec.Code != 200 {
			t.Fatalf("request %d should pass: %d", i+1, rec.Code)
		}
	}
	rec := h.do("POST", "/v1/normalize", "keyA", landmarkAddr)
	m := expectProblem(t, rec, 429, "rate_limited")
	if rec.Header().Get("Retry-After") == "" || rec.Header().Get("RateLimit-Remaining") != "0" || m["retry_after_seconds"] == nil {
		t.Errorf("429 needs Retry-After, RateLimit-Remaining: 0 and retry_after_seconds: %v / %v", rec.Header(), m)
	}
	// another tenant is unaffected
	if rec := h.do("POST", "/v1/geocode", "keyB", landmarkAddr); rec.Code != 200 {
		t.Errorf("tenant B must not share tenant A's limit: %d", rec.Code)
	}
}

func TestOperatorRateLimitOverrideWins(t *testing.T) {
	h := newHarness(t)
	two := 2
	h.mem.Tenants[tenantA] = store.TenantSettings{TrafficClass: "real", RatePerMin: &two}
	rec := h.do("POST", "/v1/normalize", "keyA", landmarkAddr)
	if rec.Header().Get("RateLimit-Limit") != "2" {
		t.Errorf("tenant override should set the limit to 2, got %q", rec.Header().Get("RateLimit-Limit"))
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/v1/normalize", "keyA", `{"address":"opp. first bank, allen avenue, ikeja, lagos"}`)
	m := decodeBody(t, rec)
	lm := m["normalized"].(map[string]any)["landmarks"].([]any)[0].(map[string]any)
	if lm["name"] != "first bank" || lm["relation"] != "opposite" {
		t.Errorf("landmark: %v", lm)
	}
	if _, has := m["result"]; has {
		t.Errorf("normalize must not return a coordinate")
	}
}

func TestBatch(t *testing.T) {
	h := newHarness(t)
	body := `{"items":[{"address":"allen avenue, ikeja","client_ref":"a"},{"address":"behind the mango tree","client_ref":"c"}]}`
	rec := h.do("POST", "/v1/geocode:batch", "keyA", body)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	results := decodeBody(t, rec)["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("one result per item, got %d", len(results))
	}
	r0, r1 := results[0].(map[string]any), results[1].(map[string]any)
	if r0["status"] != "ok" || r0["client_ref"] != "a" || r0["index"] != float64(0) {
		t.Errorf("item 0: %v", r0)
	}
	if r1["status"] != "ok" || r1["answer"].(map[string]any)["result"] != nil {
		t.Errorf("item 1 should be ok with a null result: %v", r1)
	}
}

func TestBatchRejectsTheWholeRequestWhenAnItemBreaksTheSchema(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("c", 65)
	body := `{"items":[{"address":"allen avenue"},{"address":"x"},{"address":"ikeja","client_ref":"` + long + `"}]}`
	m := expectProblem(t, h.do("POST", "/v1/geocode:batch", "keyA", body), 422, "validation_failed")
	errs := m["errors"].([]any)
	fields := []string{}
	for _, e := range errs {
		fields = append(fields, e.(map[string]any)["field"].(string))
	}
	if !contains2(fields, "items[1].address") || !contains2(fields, "items[2].client_ref") {
		t.Errorf("errors should name the offending items: %v", fields)
	}
	expectProblem(t, h.do("POST", "/v1/geocode:batch", "keyA", `{"items":[]}`), 422, "validation_failed")
}

func contains2(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestBatchCostsMoreThanOneRequest(t *testing.T) {
	h := newHarness(t)
	var items []string
	for i := 0; i < 50; i++ {
		items = append(items, `{"address":"allen avenue, ikeja"}`)
	}
	rec := h.do("POST", "/v1/geocode:batch", "keyA", `{"items":[`+strings.Join(items, ",")+`]}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// 1 for the call + 5 for 50 addresses = 6 of 60 tokens
	if rem := rec.Header().Get("RateLimit-Remaining"); rem != "54" {
		t.Errorf("50 addresses should cost 6 tokens (54 left), got %s left", rem)
	}
}

func TestCorrectionsFlow(t *testing.T) {
	h := newHarness(t)
	g := h.do("POST", "/v1/geocode", "keyA", landmarkAddr)
	reqID := decodeBody(t, g)["request_id"].(string)
	h.flush() // the request log is written asynchronously

	good := `{"request_id":"` + reqID + `","address":"opp. first bank, allen avenue, ikeja, lagos","confirmed":{"lat":6.6018,"lng":3.3515},"note":"at the gate"}`
	rec := h.do("POST", "/v1/corrections", "keyA", good)
	if rec.Code != 201 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	m := decodeBody(t, rec)
	if m["status"] != "pending" || m["id"] == "" {
		t.Errorf("correction: %v", m)
	}
	c := h.mem.Corrections[0]
	if c.OriginalPrecision != "street" || c.InputText != "" || len(c.InputHash) != 32 {
		t.Errorf("stored correction: %+v", c)
	}

	expectProblem(t, h.do("POST", "/v1/corrections", "keyA", good), 409, "duplicate_correction")
	// another tenant cannot correct tenant A's request, and learns nothing about whether it exists
	other := strings.Replace(good, "at the gate", "x", 1)
	expectProblem(t, h.do("POST", "/v1/corrections", "keyB", other), 404, "request_not_found")
	expectProblem(t, h.do("POST", "/v1/corrections", "keyA",
		`{"request_id":"99999999-9999-4999-8999-999999999999","address":"allen avenue","confirmed":{"lat":6.6,"lng":3.3}}`), 404, "request_not_found")
	// validation
	swapped := `{"request_id":"` + reqID + `","address":"allen avenue","confirmed":{"lat":3.35,"lng":6.60}}`
	m2 := expectProblem(t, h.do("POST", "/v1/corrections", "keyA", swapped), 422, "validation_failed")
	if errs := m2["errors"].([]any); len(errs) == 0 {
		t.Errorf("want field errors for swapped coordinates")
	}
	expectProblem(t, h.do("POST", "/v1/corrections", "keyA", `{"request_id":"nope","address":"allen avenue","confirmed":{"lat":6.6,"lng":3.3}}`), 422, "validation_failed")
	expectProblem(t, h.do("POST", "/v1/corrections", "keyA", `{"request_id":"`+reqID+`","address":"allen avenue"}`), 422, "validation_failed")
}

func TestCorrectionKeepsRawTextOnlyWithOptIn(t *testing.T) {
	h := newHarness(t)
	h.mem.Tenants[tenantA] = store.TenantSettings{TrafficClass: "real", RetainRaw: true}
	reqID := decodeBody(t, h.do("POST", "/v1/geocode", "keyA", landmarkAddr))["request_id"].(string)
	h.flush()
	h.do("POST", "/v1/corrections", "keyA", `{"request_id":"`+reqID+`","address":"allen avenue, ikeja","confirmed":{"lat":6.6,"lng":3.35}}`)
	c := h.mem.Corrections[0]
	if c.InputText != "allen avenue, ikeja" || c.InputTextExpiresAt == nil {
		t.Errorf("opted-in tenant: raw text should be kept with an expiry: %+v", c)
	}
}

func TestUnknownRoutesAndMethods(t *testing.T) {
	h := newHarness(t)
	expectProblem(t, h.do("GET", "/nope", "keyA", ""), 404, "not_found")
	rec := h.do("GET", "/v1/geocode", "keyA", "")
	expectProblem(t, rec, 405, "method_not_allowed")
	if rec.Header().Get("Allow") != "POST" {
		t.Errorf("Allow: %q", rec.Header().Get("Allow"))
	}
}

func TestProbes(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("GET", "/healthz", "", ""); rec.Code != 200 {
		t.Errorf("healthz needs no key: %d", rec.Code)
	}
	if rec := h.do("GET", "/readyz", "", ""); rec.Code != 200 {
		t.Errorf("readyz: %d", rec.Code)
	}
	h.mem.Fail = io.ErrClosedPipe
	expectProblem(t, h.do("GET", "/readyz", "", ""), 503, "not_ready")
}

func TestStoreOutageDoesNotBreakAnswers(t *testing.T) {
	h := newHarness(t)
	h.mem.Fail = io.ErrClosedPipe // tenant lookups and logging both fail
	rec := h.do("POST", "/v1/geocode", "keyA", landmarkAddr)
	if rec.Code != 200 {
		t.Errorf("bookkeeping failures must not fail the answer: %d %s", rec.Code, rec.Body)
	}
}

func TestMetricsAreRecorded(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/v1/geocode", "keyA", landmarkAddr)
	h.do("POST", "/v1/geocode", "", landmarkAddr)
	var b bytes.Buffer
	h.met.WritePrometheus(&b)
	out := b.String()
	for _, want := range []string{
		`geocode_http_requests_total{endpoint="geocode",status_class="2xx",traffic_class="real"} 1`,
		`geocode_http_requests_total{endpoint="geocode",status_class="4xx",traffic_class="none"} 1`,
		`geocode_results_total{precision="street",traffic_class="real"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing metric: %s\n%s", want, out)
		}
	}
}

// The contract is openapi/openapi.yaml. This fails if the router and the spec disagree.
func TestRoutesMatchTheOpenAPISpec(t *testing.T) {
	raw, err := os.ReadFile("../../openapi/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRE := regexp.MustCompile(`^  (/[^\s:][^\s]*):\s*$`)
	methodRE := regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	var spec []string
	cur := ""
	inPaths := false
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && !strings.HasPrefix(line, " "):
			inPaths = false
		case inPaths:
			if m := pathRE.FindStringSubmatch(line); m != nil {
				cur = m[1]
			} else if m := methodRE.FindStringSubmatch(line); m != nil && cur != "" {
				spec = append(spec, strings.ToUpper(m[1])+" "+cur)
			}
		}
	}
	sort.Strings(spec)
	got := newHarness(t).srv.Routes()
	if strings.Join(got, "\n") != strings.Join(spec, "\n") {
		t.Errorf("router and spec disagree\n router: %v\n spec:   %v", got, spec)
	}
}

func contains(v any, s string) bool {
	list, _ := v.([]any)
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
