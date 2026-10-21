package gazetteer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
)

// Needs a database with the migrations applied and the test map staged:
//
//	osm2pgsql -d geo_e2e -O flex -S scripts/osm2pgsql-gazetteer.lua --slim --drop testdata/mini.osm
//	TEST_DATABASE_URL='postgres://user:pw@localhost/geo_e2e?sslmode=disable' go test ./internal/gazetteer
//
// The build runs as a superuser; every query is then made as the least-privilege geocode_api role.
func connect(t *testing.T) (admin, api *sql.DB) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	admin, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	var reg sql.NullString
	if err := admin.QueryRow(`SELECT to_regclass('osm_stage.roads')::text`).Scan(&reg); err != nil || !reg.Valid {
		t.Skip("osm_stage not loaded: run osm2pgsql on testdata/mini.osm first")
	}
	api, err = sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	api.SetMaxOpenConns(1)
	if _, err := api.Exec(`SET ROLE geocode_api`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(); api.Close() })
	return admin, api
}

func buildTestVersion(t *testing.T, admin *sql.DB, norm *resolver.Normalizer) Report {
	t.Helper()
	ctx := context.Background()
	prevID, _, prevErr := ActiveVersion(ctx, admin)
	rep, err := Build(ctx, admin, Options{Label: fmt.Sprintf("itest-%d", time.Now().UnixNano()),
		ADM1Path: "../../testdata/adm1.geojson", ADM2Path: "../../testdata/adm2.geojson", Norm: norm, Activate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if prevErr == nil {
			admin.Exec(`UPDATE gaz.active_version SET data_version_id = $1`, prevID)
		} else {
			admin.Exec(`DELETE FROM gaz.active_version`)
		}
		admin.Exec(`DELETE FROM gaz.data_versions WHERE id = $1`, rep.VersionID)
	})
	return rep
}

func TestBuildProducesTheExpectedGazetteer(t *testing.T) {
	admin, _ := connect(t)
	norm := resolver.NewNormalizer(resolver.DefaultTerms())
	rep := buildTestVersion(t, admin, norm)

	want := map[string]int{"state": 2, "lga": 3, "road": 2, "locality": 5, "place": 5, "address": 1}
	for k, n := range want {
		if rep.Counts[k] != n {
			t.Errorf("%s: got %d want %d (all: %v)", k, rep.Counts[k], n, rep.Counts)
		}
	}
	if rep.Unassigned != 0 {
		t.Errorf("every feature in the test map lies inside an LGA, got %d unassigned", rep.Unassigned)
	}
	if !rep.Activated {
		t.Errorf("build should be active")
	}

	// the footway is not a road for cars; roads and places were assigned to the right LGA and state
	check := func(query, wantLGA, wantState string) {
		t.Helper()
		var lga, state sql.NullString
		err := admin.QueryRow(query, rep.VersionID).Scan(&lga, &state)
		if err != nil || lga.String != wantLGA || state.String != wantState {
			t.Errorf("%s\n got lga=%q state=%q err=%v, want %q / %q", query, lga.String, state.String, err, wantLGA, wantState)
		}
	}
	base := `SELECT ln.name_norm, sn.name_norm FROM gaz.features f
	  LEFT JOIN gaz.names ln ON ln.feature_id = f.lga_id AND ln.is_primary
	  LEFT JOIN gaz.names sn ON sn.feature_id = f.state_id AND sn.is_primary
	  JOIN gaz.names n ON n.feature_id = f.id WHERE f.data_version_id = $1 AND `
	check(base+`f.kind = 'road' AND n.name_norm = 'allen avenue'`, "ikeja", "lagos")
	check(base+`f.kind = 'locality' AND n.name_norm = 'garki'`, "abuja municipal", "federal capital territory")
	check(base+`f.kind = 'place' AND n.name_norm = 'first bank' AND ST_Y(f.rep_point) < 6.5`, "eti osa", "lagos")

	// aliases become searchable names, and the same key is stored once per feature
	var n int
	admin.QueryRow(`SELECT count(*) FROM gaz.names x JOIN gaz.features f ON f.id = x.feature_id
	   WHERE f.data_version_id = $1 AND x.name_norm = 'allen avenue'`, rep.VersionID).Scan(&n)
	if n != 1 {
		t.Errorf("Allen Avenue and its alias Allen Ave share one key, stored once; got %d rows", n)
	}
	admin.QueryRow(`SELECT count(*) FROM gaz.names x JOIN gaz.features f ON f.id = x.feature_id
	   WHERE f.data_version_id = $1 AND x.name_norm = 'v i'`, rep.VersionID).Scan(&n)
	if n != 1 {
		t.Errorf("the V.I. alias should be searchable as 'v i', got %d", n)
	}
}

func TestBuildRejectsUnknownStateNamesAndKeepsNothing(t *testing.T) {
	admin, _ := connect(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "adm1.geojson")
	os.WriteFile(bad, []byte(`{"type":"FeatureCollection","features":[{"type":"Feature","properties":{"shapeName":"Atlantis","shapeID":"x"},
	  "geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}}]}`), 0o600)
	label := fmt.Sprintf("itest-bad-%d", time.Now().UnixNano())
	_, err := Build(context.Background(), admin, Options{Label: label, ADM1Path: bad, ADM2Path: "../../testdata/adm2.geojson",
		Norm: resolver.NewNormalizer(resolver.DefaultTerms())})
	if err == nil || !strings.Contains(err.Error(), "Atlantis") {
		t.Fatalf("want an error naming the unknown state, got %v", err)
	}
	var n int
	admin.QueryRow(`SELECT count(*) FROM gaz.data_versions WHERE label = $1`, label).Scan(&n)
	if n != 0 {
		t.Errorf("a failed build must leave no version behind")
	}
}

func TestResolveThroughThePipeline(t *testing.T) {
	admin, api := connect(t)
	norm := resolver.NewNormalizer(resolver.DefaultTerms())
	buildTestVersion(t, admin, norm)
	pipe := resolver.NewPipeline(norm, NewSource(api, norm))
	pipe.SourceTimeout = 10 * time.Second // a cold database must not turn a correct answer into a timeout
	geocode := func(addr string) resolver.Answer {
		t.Helper()
		return pipe.Geocode(context.Background(), addr, resolver.Hints{}, 10)
	}
	near := func(a resolver.Answer, lat, lng, withinM float64) bool {
		return a.Fix != nil && haversine(point{a.Fix.Lat, a.Fix.Lng}, point{lat, lng}) <= withinM
	}
	has := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}

	t.Run("landmark, street, area and state agree: street-level, near the right bank", func(t *testing.T) {
		a := geocode("opp. first bank, allen avenue, ikeja, lagos")
		if a.Fix == nil || a.Fix.Precision != resolver.Street {
			t.Fatalf("want a street fix, got %+v (candidates %+v)", a.Fix, a.Candidates)
		}
		if !near(a, 6.6020, 3.3520, 300) {
			t.Errorf("the pin should be within 300 m of the Ikeja bank, got %+v", a.Fix)
		}
		if has(a.Warnings, "landmark_not_matched") {
			t.Errorf("the bank exists in the data: %v", a.Warnings)
		}
	})

	t.Run("house number on a known street: building-level", func(t *testing.T) {
		a := geocode("12 allen avenue, ikeja, lagos")
		if a.Fix == nil || a.Fix.Precision != resolver.Building || !near(a, 6.5990, 3.3480, 20) {
			t.Fatalf("want a building fix at the address point, got %+v", a.Fix)
		}
	})

	t.Run("a bank name alone is ambiguous: no claim, with a reason", func(t *testing.T) {
		a := geocode("first bank")
		if a.Fix != nil {
			t.Fatalf("two banks share this name 20 km apart; claiming one would be a guess: %+v", a.Fix)
		}
		if !has(a.Warnings, "low_confidence") {
			t.Errorf("want low_confidence, got %v", a.Warnings)
		}
		if len(a.Candidates) == 0 || !a.Candidates[0].Ambiguous {
			t.Errorf("the candidate should be flagged ambiguous: %+v", a.Candidates)
		}
	})

	t.Run("a place in another state is not matched when the state is given", func(t *testing.T) {
		a := geocode("ikeja, abuja")
		if a.Fix == nil || a.Fix.Precision != resolver.State || !near(a, 9.0765, 7.3986, 60000) {
			t.Fatalf("Ikeja is in Lagos, so this should fall back to the FCT state: %+v", a.Fix)
		}
	})

	t.Run("neighbourhood in the capital", func(t *testing.T) {
		a := geocode("garki, abuja")
		if a.Fix == nil || a.Fix.Precision != resolver.Locality || !near(a, 9.0300, 7.4900, 500) {
			t.Fatalf("want a locality fix at Garki, got %+v", a.Fix)
		}
	})

	t.Run("an alias name resolves", func(t *testing.T) {
		a := geocode("V.I., lagos")
		if a.Fix == nil || a.Fix.Precision != resolver.Locality || !near(a, 6.4281, 3.4219, 500) {
			t.Fatalf("V.I. is an alias of Victoria Island: %+v", a.Fix)
		}
	})

	t.Run("a misspelled street is found but not over-claimed", func(t *testing.T) {
		a := geocode("allen avenu, ikeja")
		found := false
		for _, c := range a.Candidates {
			if c.Precision == resolver.Street && c.Confidence < 0.8 {
				found = true
			}
		}
		if !found {
			t.Errorf("the typo should still surface a street candidate with reduced confidence: %+v", a.Candidates)
		}
		if a.Fix != nil && a.Fix.Precision == resolver.Building {
			t.Errorf("a typo must never produce a building-level claim")
		}
	})

	t.Run("a landmark far from the named street is a different place: never pinned", func(t *testing.T) {
		a := geocode("opp. zenith bank, allen avenue, ikeja, lagos")
		if a.Fix == nil || near(a, 6.6240, 3.3540, 1000) {
			t.Fatalf("the only Zenith Bank is 2.5 km from Allen Avenue: the pin must stay on the road, not jump to the bank: %+v", a.Fix)
		}
		for _, c := range a.Candidates {
			for _, m := range c.Matched {
				if m == "landmark:zenith bank" && c.Confidence > 0.3 {
					t.Errorf("a landmark 2.5 km from the street should score very low, got %v", c.Confidence)
				}
			}
		}
	})

	t.Run("Roman numerals match their digits", func(t *testing.T) {
		a := geocode("wuse 2, abuja")
		if a.Fix == nil || a.Fix.Precision != resolver.Locality || !near(a, 9.0700, 7.4700, 300) {
			t.Fatalf("Wuse 2 is Wuse II: %+v", a.Fix)
		}
	})

	t.Run("the specific neighbourhood beats the city named after it", func(t *testing.T) {
		a := geocode("wusee 2, abuja city")
		if a.Fix == nil || !near(a, 9.0700, 7.4700, 500) {
			t.Fatalf("a slightly misspelt Wuse II should still beat the city centre: %+v", a.Fix)
		}
	})

	t.Run("an unclassified name that is a known place is found", func(t *testing.T) {
		a := geocode("computer village, ikeja")
		if a.Fix == nil || a.Fix.Precision != resolver.Street || !near(a, 6.5995, 3.3505, 300) {
			t.Fatalf("Computer Village is a mapped place: %+v", a.Fix)
		}
	})

	t.Run("nothing resolvable returns no pin", func(t *testing.T) {
		a := geocode("behind the mango tree")
		if a.Fix != nil || len(a.Candidates) != 0 || !has(a.Warnings, "landmark_not_matched") {
			t.Errorf("want no fix, no candidates, landmark_not_matched: %+v", a)
		}
	})
}

func TestWatcherFollowsNewBuildsWithoutARestart(t *testing.T) {
	admin, _ := connect(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// start from "nothing active"
	var prevID sql.NullInt64
	admin.QueryRow(`SELECT data_version_id FROM gaz.active_version`).Scan(&prevID)
	admin.Exec(`DELETE FROM gaz.active_version`)
	t.Cleanup(func() {
		if prevID.Valid {
			admin.Exec(`INSERT INTO gaz.active_version (singleton, data_version_id) VALUES (true, $1) ON CONFLICT (singleton) DO UPDATE SET data_version_id = EXCLUDED.data_version_id`, prevID.Int64)
		}
	})

	w := Watch(ctx, admin, 40*time.Millisecond, nil)
	if _, ok := w.Label(); ok {
		t.Fatalf("nothing is active, so the watcher must say not ready")
	}
	norm := resolver.NewNormalizer(resolver.DefaultTerms())
	rep := buildTestVersion(t, admin, norm) // activates its build
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if label, ok := w.Label(); ok {
			if label != rep.Label {
				t.Fatalf("label %q, want %q", label, rep.Label)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the watcher never noticed the activated build")
}
