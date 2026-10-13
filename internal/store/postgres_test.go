package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
)

// These tests need a database with the migrations applied. Set TEST_DATABASE_URL to run them, e.g.
//
//	TEST_DATABASE_URL='postgres://postgres@localhost/geo_test?sslmode=disable' go test ./internal/store
//
// The connection runs as the least-privilege API role, so passing also proves the grants suffice.
func testStore(t *testing.T) *Postgres {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // one session, so SET ROLE applies to every statement
	if _, err := db.Exec("SET ROLE geocode_api"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Postgres{DB: db}
}

const (
	tenantA = "aaaaaaaa-0000-4000-8000-000000000001"
	tenantB = "bbbbbbbb-0000-4000-8000-000000000002"
)

func TestPostgresTenantRequestCorrectionFlow(t *testing.T) {
	ctx := context.Background()
	p := testStore(t)
	suffix := time.Now().Format("150405.000")

	// first sight creates the tenant with safe defaults; the second call is idempotent
	ts, err := p.EnsureTenant(ctx, tenantA, "Tenant A "+suffix)
	if err != nil {
		t.Fatal(err)
	}
	if ts.TrafficClass != "real" || ts.RetainRaw || ts.GoogleEnabled || ts.NDAPSEnabled || ts.RatePerMin != nil {
		t.Errorf("new tenants must start with safe defaults: %+v", ts)
	}
	if _, err := p.EnsureTenant(ctx, tenantA, "Tenant A renamed "+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := p.EnsureTenant(ctx, tenantB, "Tenant B"); err != nil {
		t.Fatal(err)
	}

	// log a request, including nullable fields and arrays
	reqID := "cccccccc-0000-4000-8000-" + time.Now().Format("150405000000")
	conf := 0.71
	rec := RequestRecord{ID: reqID, ReceivedAt: time.Now().UTC(), TenantID: tenantA, KeyID: "key_1",
		Endpoint: "geocode", TrafficClass: "real", Status: 200, LatencyMS: 42, InputHash: []byte{1, 2, 3},
		Precision: "street", Confidence: &conf, StateID: 25, LGAName: "ikeja",
		SourcesUsed: []string{"fixture"}, Warnings: []string{"landmark_not_matched"}}
	if err := p.LogRequest(ctx, rec); err != nil {
		t.Fatalf("log request: %v", err)
	}
	// a failed request with nothing resolved: nulls everywhere they are allowed
	failed := RequestRecord{ID: "dddddddd-0000-4000-8000-" + time.Now().Format("150405000000"), ReceivedAt: time.Now().UTC(),
		TenantID: tenantA, KeyID: "key_1", Endpoint: "geocode", TrafficClass: "real", Status: 422, LatencyMS: 1,
		ErrorCode: "validation_failed"}
	if err := p.LogRequest(ctx, failed); err != nil {
		t.Fatalf("log failed request: %v", err)
	}

	// ownership: the owner finds it, another tenant does not
	info, ok, err := p.FindRequest(ctx, tenantA, reqID)
	if err != nil || !ok || info.Precision != "street" || info.Confidence == nil || *info.Confidence != 0.71 {
		t.Fatalf("owner lookup: %+v ok=%v err=%v", info, ok, err)
	}
	if _, ok, _ := p.FindRequest(ctx, tenantB, reqID); ok {
		t.Errorf("another tenant must not see this request")
	}

	// corrections: stored once, duplicates refused
	c := Correction{ID: "eeeeeeee-0000-4000-8000-" + time.Now().Format("150405000000"), TenantID: tenantA, RequestID: reqID,
		InputHash: []byte{1, 2, 3}, NormalizedJSON: []byte(`{"area":"ikeja"}`), OriginalPrecision: "street",
		OriginalConfidence: &conf, Lat: 6.6018, Lng: 3.3515, Note: "confirmed at the gate"}
	if _, err := p.InsertCorrection(ctx, c); err != nil {
		t.Fatalf("insert correction: %v", err)
	}
	c.ID = "ffffffff-0000-4000-8000-" + time.Now().Format("150405000000")
	if _, err := p.InsertCorrection(ctx, c); !errors.Is(err, ErrDuplicate) {
		t.Errorf("second correction for the same request must be ErrDuplicate, got %v", err)
	}

	// raw text of an opted-in tenant, within the 30-day limit
	now := time.Now().UTC()
	if err := p.LogRawText(ctx, RawText{RequestID: reqID, ReceivedAt: now, Text: "opp. first bank", ExpiresAt: now.Add(30 * 24 * time.Hour)}); err != nil {
		t.Errorf("raw text: %v", err)
	}
	if err := p.LogRawText(ctx, RawText{RequestID: reqID, ReceivedAt: now, Text: "x", ExpiresAt: now.Add(31 * 24 * time.Hour)}); err == nil {
		t.Errorf("the database must refuse raw text kept longer than 30 days")
	}
	if err := p.Ping(ctx); err != nil {
		t.Error(err)
	}
}

// The Go dictionary and the database tables must say the same thing.
func TestEmbeddedDictionaryMatchesDatabase(t *testing.T) {
	p := testStore(t)
	terms := resolver.DefaultTerms()

	rows, err := p.DB.Query(`SELECT id, name, name_norm, aliases FROM ref.states ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []resolver.StateInfo
	for rows.Next() {
		var s resolver.StateInfo
		var aliases []sql.NullString
		var raw []byte
		if err := rows.Scan(&s.ID, &s.Name, &s.Norm, &raw); err != nil {
			t.Fatal(err)
		}
		_ = aliases
		s.Aliases = parsePGArray(string(raw))
		got = append(got, s)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}

	if len(got) != len(terms.States) {
		t.Fatalf("states: database has %d, Go has %d", len(got), len(terms.States))
	}
	for i, s := range got {
		w := terms.States[i]
		sort.Strings(s.Aliases)
		sort.Strings(w.Aliases)
		if s.ID != w.ID || s.Name != w.Name || s.Norm != w.Norm || !(len(s.Aliases) == 0 && len(w.Aliases) == 0 || reflect.DeepEqual(s.Aliases, w.Aliases)) {
			t.Errorf("state %d differs:\n db %+v\n go %+v", s.ID, s, w)
		}
	}

	for kind, want := range map[string]map[string]string{
		"abbreviation": terms.Abbreviations, "relation": terms.Relations, "feature_word": terms.FeatureWords,
	} {
		rows, err := p.DB.Query(`SELECT term, expansion FROM ref.address_terms WHERE kind = $1::ref.term_kind`, kind)
		if err != nil {
			t.Fatal(err)
		}
		have := map[string]string{}
		for rows.Next() {
			var term, exp string
			rows.Scan(&term, &exp)
			have[term] = exp
		}
		if rows.Err() != nil {
			t.Fatal(rows.Err())
		}

		rows.Close()

		if !reflect.DeepEqual(have, want) {
			t.Errorf("%s terms differ between database and Go:\n db %v\n go %v", kind, have, want)
		}
	}
}

// parsePGArray reads simple one-dimensional text arrays such as {fct,abuja,"fct abuja"}.
func parsePGArray(s string) []string {
	s = s[1 : len(s)-1]
	if s == "" {
		return nil
	}
	var out []string
	cur, quoted := "", false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			out = append(out, cur)
			cur = ""
		default:
			cur += string(r)
		}
	}
	return append(out, cur)
}
