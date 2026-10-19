package evaluate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/gazetteer"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
)

func norm() *resolver.Normalizer { return resolver.NewNormalizer(resolver.DefaultTerms()) }

func TestReadCSVAcceptsTheSample(t *testing.T) {
	f, err := os.Open("../../testdata/eval-sample.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cases, err := ReadCSV(f, norm())
	if err != nil || len(cases) != 6 {
		t.Fatalf("got %d cases, err %v", len(cases), err)
	}
	if cases[2].StateID != 15 || cases[0].StateID != 25 {
		t.Errorf("state ids: %d %d", cases[2].StateID, cases[0].StateID)
	}
}

func TestReadCSVRefusesBadRows(t *testing.T) {
	head := "raw_text,lat,lng,expected_precision,truth_method,city,state,style\n"
	bad := map[string]string{
		"swapped coordinates":       `"allen avenue",3.35,6.60,street,door_step_gps,,,`,
		"truth from google":         `"allen avenue",6.60,3.35,street,google_maps_pin,,,`,
		"only latitude":             `"allen avenue",6.60,,street,door_step_gps,,,`,
		"coordinates without truth": `"allen avenue",6.60,3.35,,,,,`,
		"unknown precision":         `"allen avenue",6.60,3.35,exact,door_step_gps,,,`,
		"unknown state":             `"allen avenue",6.60,3.35,street,door_step_gps,,Atlantis,`,
		"unknown style":             `"allen avenue",6.60,3.35,street,door_step_gps,,,fancy`,
		"too short":                 `"ab",6.60,3.35,street,door_step_gps,,,`,
		"precision without truth":   `"allen avenue",,,street,,,,`,
	}
	for name, row := range bad {
		if _, err := ReadCSV(strings.NewReader(head+row+"\n"), norm()); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	if _, err := ReadCSV(strings.NewReader("address\nx\n"), norm()); err == nil {
		t.Errorf("missing raw_text header should be refused")
	}
	if _, err := ReadCSV(strings.NewReader(head+`"garki, abuja",,,,,,,`+"\n"), norm()); err != nil {
		t.Errorf("a text-only row (tier B) is valid: %v", err)
	}
}

func TestImportRunReportAgainstTheRealGazetteer(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var reg sql.NullString
	if db.QueryRow(`SELECT to_regclass('osm_stage.roads')::text`).Scan(&reg); !reg.Valid {
		t.Skip("osm_stage not loaded")
	}
	n := norm()
	rep, err := gazetteer.Build(ctx, db, gazetteer.Options{Label: fmt.Sprintf("itest-eval-%d", time.Now().UnixNano()),
		ADM1Path: "../../testdata/adm1.geojson", ADM2Path: "../../testdata/adm2.geojson", Norm: n, Activate: true})
	if err != nil {
		t.Fatal(err)
	}
	prev := int64(0)
	t.Cleanup(func() { db.Exec(`DELETE FROM gaz.data_versions WHERE id = $1`, rep.VersionID); _ = prev })

	f, _ := os.Open("../../testdata/eval-sample.csv")
	cases, err := ReadCSV(f, n)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	setName := fmt.Sprintf("itest-%d", time.Now().UnixNano())
	t.Cleanup(func() { db.Exec(`DELETE FROM eval.sets WHERE name = $1`, setName) })
	if _, err := Import(ctx, db, SetInfo{Name: setName, Tier: "A", Source: "unit test"}, cases); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(ctx, db, SetInfo{Name: setName, Tier: "A", Source: "again"}, cases); err == nil {
		t.Errorf("importing the same set name twice must fail")
	}

	pipe := resolver.NewPipeline(n, gazetteer.NewSource(db, n))
	pipe.SourceTimeout = 10 * time.Second
	runID, err := Run(ctx, db, pipe, RunOptions{Set: setName, GitSHA: "test", VersionID: rep.VersionID})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Report(ctx, db, runID)
	if err != nil {
		t.Fatal(err)
	}
	if s.N != 6 || s.Resolved != 4 {
		t.Fatalf("want 6 cases, 4 resolved: %+v", s)
	}
	if s.MedianErrorM == nil || *s.MedianErrorM > 150 {
		t.Errorf("median error should be small on this map: %v", s.MedianErrorM)
	}
	if s.Within500 == nil || *s.Within500 != 1 {
		t.Errorf("every resolved answer should be within 500 m: %v", s.Within500)
	}
	if s.FalseConfidence == nil || *s.FalseConfidence != 0 || s.Calibration == nil || *s.Calibration != 1 {
		t.Errorf("no confident answer should be wrong here: cal=%v fc=%v", s.Calibration, s.FalseConfidence)
	}
	if s.Mix["none"] != 2 {
		t.Errorf("the mango tree and the bare First Bank should be unresolved: %v", s.Mix)
	}
	out := s.String()
	for _, want := range []string{"resolved             4 of 6", "meets target", "largest errors"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}

	// tier B: text only, no accuracy figures
	tb := setName + "-b"
	t.Cleanup(func() { db.Exec(`DELETE FROM eval.sets WHERE name = $1`, tb) })
	textOnly, _ := ReadCSV(strings.NewReader("raw_text\n\"garki, abuja\"\n\"allen avenue, ikeja\"\n"), n)
	if _, err := Import(ctx, db, SetInfo{Name: tb, Tier: "B", Source: "web pages"}, textOnly); err != nil {
		t.Fatal(err)
	}
	rb, err := Run(ctx, db, pipe, RunOptions{Set: tb, GitSHA: "test", VersionID: rep.VersionID})
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := Report(ctx, db, rb)
	if sb.MedianErrorM != nil || !strings.Contains(sb.String(), "no ground truth") {
		t.Errorf("tier B must not claim accuracy:\n%s", sb.String())
	}
	if _, err := Import(ctx, db, SetInfo{Name: tb + "x", Tier: "B", Source: "x"}, cases); err == nil {
		t.Errorf("tier B with coordinates must be refused")
	}
}
