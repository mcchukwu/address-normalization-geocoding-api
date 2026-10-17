// Package evaluate measures the geocoder on a set of addresses. Tier A sets carry true
// coordinates collected on the ground; tier B sets are address text only (resolution rate and
// precision mix, no accuracy); tier C sets use independently collected coordinates at scale.
// Results go to the eval schema, and the numbers come from the SQL views, so the same figures
// can be recomputed at any time from the stored points.
package evaluate

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
)

// Case is one address to evaluate. Lat and Lng are nil for tier B.
type Case struct {
	Raw, City, State, Style, Truth, Precision, CollectedAt, Notes, URL string
	Lat, Lng                                                           *float64
	StateID                                                            int
}

var precisions = map[string]bool{"building": true, "street": true, "locality": true, "lga": true, "state": true}
var styles = map[string]bool{"structured": true, "landmark": true, "mixed": true, "poi_name": true, "postcode": true}

// ReadCSV reads cases. Header: raw_text,lat,lng,expected_precision,truth_method,city,state,style,collected_at,notes,source_url
// (only raw_text is required; truth columns must be filled together or left empty).
func ReadCSV(r io.Reader, norm *resolver.Normalizer) ([]Case, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	if _, ok := col["raw_text"]; !ok {
		return nil, errors.New("header must include raw_text")
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	var out []Case
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		c := Case{Raw: get(rec, "raw_text"), City: get(rec, "city"), State: get(rec, "state"), Style: strings.ToLower(get(rec, "style")),
			Truth: get(rec, "truth_method"), Precision: strings.ToLower(get(rec, "expected_precision")),
			CollectedAt: get(rec, "collected_at"), Notes: get(rec, "notes"), URL: get(rec, "source_url")}
		fail := func(msg string) error { return fmt.Errorf("line %d (%q): %s", line, c.Raw, msg) }
		if len(strings.TrimSpace(c.Raw)) < 3 {
			return nil, fail("raw_text is too short")
		}
		latS, lngS := get(rec, "lat"), get(rec, "lng")
		if (latS == "") != (lngS == "") {
			return nil, fail("give both lat and lng, or neither")
		}
		hasTruth := latS != ""
		if hasTruth {
			lat, e1 := strconv.ParseFloat(latS, 64)
			lng, e2 := strconv.ParseFloat(lngS, 64)
			if e1 != nil || e2 != nil {
				return nil, fail("lat and lng must be numbers")
			}
			if lat < 4.0 || lat > 14.5 || lng < 2.0 || lng > 15.0 {
				return nil, fail("lat/lng is outside Nigeria: are they swapped? (lat first, about 4 to 14)")
			}
			c.Lat, c.Lng = &lat, &lng
			if !precisions[c.Precision] {
				return nil, fail("expected_precision must be one of building, street, locality, lga, state")
			}
			if c.Truth == "" {
				return nil, fail("truth_method is required with coordinates (e.g. door_step_gps)")
			}
			if strings.Contains(strings.ToLower(c.Truth), "google") {
				return nil, fail("ground truth must not come from Google (circular and against its terms)")
			}
		} else if c.Precision != "" || c.Truth != "" {
			return nil, fail("expected_precision and truth_method need lat and lng")
		}
		if c.Style != "" && !styles[c.Style] {
			return nil, fail("style must be one of structured, landmark, mixed, poi_name, postcode")
		}
		if c.State != "" {
			si, ok := norm.StateByName(c.State)
			if !ok {
				return nil, fail("unknown state " + strconv.Quote(c.State))
			}
			c.StateID = si.ID
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("no cases found")
	}
	return out, nil
}

// SetInfo describes a set to create.
type SetInfo struct{ Name, Tier, Source, Description, Licence string }

// Import creates the set and its cases in one transaction.
func Import(ctx context.Context, db *sql.DB, s SetInfo, cases []Case) (int, error) {
	if s.Tier != "A" && s.Tier != "B" && s.Tier != "C" {
		return 0, errors.New("tier must be A, B or C")
	}
	for _, c := range cases {
		if s.Tier == "B" && c.Lat != nil {
			return 0, errors.New("tier B sets must not carry coordinates: web pages give text, not truth")
		}
		if s.Tier != "B" && c.Lat == nil {
			return 0, fmt.Errorf("tier %s needs coordinates for every case (%q has none)", s.Tier, c.Raw)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var setID int
	if err := tx.QueryRowContext(ctx, `INSERT INTO eval.sets (name, tier, source, description, licence) VALUES ($1, $2::eval.tier, $3, NULLIF($4,''), NULLIF($5,'')) RETURNING id`,
		s.Name, s.Tier, s.Source, s.Description, s.Licence).Scan(&setID); err != nil {
		return 0, fmt.Errorf("create set %q (does it already exist?): %w", s.Name, err)
	}
	for _, c := range cases {
		var lat, lng any
		var prec, truth any
		if c.Lat != nil {
			lat, lng, prec, truth = *c.Lat, *c.Lng, c.Precision, c.Truth
		}
		var stateID, style, collected any
		if c.StateID != 0 {
			stateID = c.StateID
		}
		if c.Style != "" {
			style = c.Style
		}
		if c.CollectedAt != "" {
			collected = c.CollectedAt
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO eval.cases (set_id, raw_text, city, state_id, style, expected, expected_precision, truth_method, collected_at, source_url, notes)
			VALUES ($1, $2, NULLIF($3,''), $4, $5::eval.address_style,
			  CASE WHEN $6::float8 IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($7::float8, $6::float8), 4326) END,
			  $8::ref.precision, $9, $10::date, NULLIF($11,''), NULLIF($12,''))`,
			setID, c.Raw, c.City, stateID, style, lat, lng, prec, truth, collected, c.URL, c.Notes)
		if err != nil {
			return 0, fmt.Errorf("insert %q: %w", c.Raw, err)
		}
	}
	return len(cases), tx.Commit()
}

// RunOptions control one evaluation run.
type RunOptions struct {
	Set       string
	GitSHA    string
	VersionID int    // gazetteer build in use, 0 if unknown
	Config    string // JSON describing sources and thresholds
}

// Run geocodes every case in the set with the pipeline and stores the results. Cases are given
// no hints: the evaluation is of the text alone, as a customer's raw input would be.
func Run(ctx context.Context, db *sql.DB, pipe *resolver.Pipeline, o RunOptions) (int64, error) {
	var setID int
	if err := db.QueryRowContext(ctx, `SELECT id FROM eval.sets WHERE name = $1`, o.Set).Scan(&setID); err != nil {
		return 0, fmt.Errorf("set %q: %w", o.Set, err)
	}
	rows, err := db.QueryContext(ctx, `SELECT id, raw_text FROM eval.cases WHERE set_id = $1 ORDER BY id`, setID)
	if err != nil {
		return 0, err
	}
	type cs struct {
		id  int64
		raw string
	}
	var cases []cs
	for rows.Next() {
		var c cs
		if err := rows.Scan(&c.id, &c.raw); err != nil {
			rows.Close()
			return 0, err
		}
		cases = append(cases, c)
	}
	rows.Close()
	if len(cases) == 0 {
		return 0, fmt.Errorf("set %q has no cases", o.Set)
	}

	cfg := o.Config
	if cfg == "" {
		cfg = "{}"
	}
	var vid any
	if o.VersionID != 0 {
		vid = o.VersionID
	}
	var runID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO eval.runs (set_id, git_sha, data_version_id, config) VALUES ($1, $2, $3, $4::jsonb) RETURNING id`,
		setID, o.GitSHA, vid, cfg).Scan(&runID); err != nil {
		return 0, err
	}
	for _, c := range cases {
		start := time.Now()
		ans := pipe.Geocode(ctx, c.raw, resolver.Hints{}, 3)
		ms := int(time.Since(start).Milliseconds())
		prec, conf := string(resolver.None), any(nil)
		var lat, lng any
		if ans.Fix != nil {
			prec, conf, lat, lng = string(ans.Fix.Precision), ans.Fix.Confidence, ans.Fix.Lat, ans.Fix.Lng
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO eval.results (run_id, case_id, precision, confidence, result, sources, warnings, latency_ms)
			VALUES ($1, $2, $3::ref.precision, $4, CASE WHEN $5::float8 IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($6::float8, $5::float8), 4326) END,
			        $7::text[], $8::text[], $9)`,
			runID, c.id, prec, conf, lat, lng, pgArray(ans.SourcesUsed), pgArray(ans.Warnings), ms); err != nil {
			return runID, err
		}
	}
	_, err = db.ExecContext(ctx, `UPDATE eval.runs SET finished_at = now() WHERE id = $1`, runID)
	return runID, err
}

func pgArray(s []string) string {
	q := make([]string, len(s))
	for i, x := range s {
		q[i] = `"` + strings.ReplaceAll(x, `"`, `\"`) + `"`
	}
	return "{" + strings.Join(q, ",") + "}"
}

// Summary is what a run achieved. Pointer fields are nil when there is nothing to measure
// (for example no ground truth in a tier B set).
type Summary struct {
	RunID, SetID                             int64
	Set, Tier, Gazetteer                     string
	N, Resolved                              int
	MedianErrorM                             *float64
	Within50, Within100, Within500, Within1k *float64
	ConfidentN                               int
	Calibration, FalseConfidence             *float64
	Mix                                      map[string]int
	Worst                                    []WorstCase
}

// WorstCase is a resolved address that landed far from the truth.
type WorstCase struct {
	Raw       string
	ErrorM    float64
	Precision string
	Conf      float64
}

// Targets from the requirements document.
const (
	TargetCalibration     = 0.90 // among confidence >= 0.8, at least this share inside the claimed radius
	TargetFalseConfidence = 0.05 // share of confident answers more than 500 m off, at most
)

// Report reads the numbers for a run from the database views.
func Report(ctx context.Context, db *sql.DB, runID int64) (Summary, error) {
	s := Summary{RunID: runID, Mix: map[string]int{}}
	var g sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT st.id, st.name, st.tier::text, d.label FROM eval.runs r JOIN eval.sets st ON st.id = r.set_id
		LEFT JOIN gaz.data_versions d ON d.id = r.data_version_id WHERE r.id = $1`, runID).Scan(&s.SetID, &s.Set, &s.Tier, &g)
	if err != nil {
		return s, fmt.Errorf("run %d: %w", runID, err)
	}
	s.Gazetteer = g.String
	var med, w50, w100, w500, w1k, cal, fc sql.NullFloat64
	err = db.QueryRowContext(ctx, `SELECT n, resolved, median_error_m, within_50m, within_100m, within_500m, within_1km, confident_n, calibration_at_0_8, false_confidence_rate
		FROM eval.run_metrics WHERE run_id = $1`, runID).Scan(&s.N, &s.Resolved, &med, &w50, &w100, &w500, &w1k, &s.ConfidentN, &cal, &fc)
	if err != nil {
		return s, err
	}
	s.MedianErrorM, s.Within50, s.Within100, s.Within500, s.Within1k = nf(med), nf(w50), nf(w100), nf(w500), nf(w1k)
	s.Calibration, s.FalseConfidence = nf(cal), nf(fc)

	mix, err := db.QueryContext(ctx, `SELECT precision::text, count(*) FROM eval.results WHERE run_id = $1 GROUP BY 1`, runID)
	if err != nil {
		return s, err
	}
	for mix.Next() {
		var p string
		var n int
		mix.Scan(&p, &n)
		s.Mix[p] = n
	}
	mix.Close()

	worst, err := db.QueryContext(ctx, `
		SELECT c.raw_text, sr.error_m, sr.precision::text, coalesce(sr.confidence, 0)
		FROM eval.scored_results sr JOIN eval.cases c ON c.id = sr.case_id
		WHERE sr.run_id = $1 AND sr.error_m IS NOT NULL ORDER BY sr.error_m DESC LIMIT 8`, runID)
	if err != nil {
		return s, err
	}
	defer worst.Close()
	for worst.Next() {
		var w WorstCase
		worst.Scan(&w.Raw, &w.ErrorM, &w.Precision, &w.Conf)
		s.Worst = append(s.Worst, w)
	}
	return s, nil
}

func nf(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

func pct(f *float64) string {
	if f == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", math.Round(*f*100))
}

// String renders the summary for a terminal.
func (s Summary) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "run %d on set %q (tier %s, %d cases)", s.RunID, s.Set, s.Tier, s.N)
	if s.Gazetteer != "" {
		fmt.Fprintf(&b, ", gazetteer %s", s.Gazetteer)
	}
	fmt.Fprintf(&b, "\n\n  resolved             %d of %d (%.0f%%)\n", s.Resolved, s.N, 100*float64(s.Resolved)/math.Max(1, float64(s.N)))
	mix := []string{}
	for _, p := range []string{"building", "street", "locality", "lga", "state", "none"} {
		if n := s.Mix[p]; n > 0 {
			mix = append(mix, fmt.Sprintf("%s %d", p, n))
		}
	}
	fmt.Fprintf(&b, "  precision mix        %s\n", strings.Join(mix, ", "))
	if s.MedianErrorM == nil {
		b.WriteString("\n  no ground truth in this set, so no accuracy figures (resolution rate and mix only)\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  median error         %.0f m (resolved answers)\n", *s.MedianErrorM)
	fmt.Fprintf(&b, "  within 50/100/500 m  %s / %s / %s   within 1 km %s\n", pct(s.Within50), pct(s.Within100), pct(s.Within500), pct(s.Within1k))
	status := func(ok bool) string {
		if ok {
			return "meets target"
		}
		return "BELOW TARGET"
	}
	if s.Calibration != nil {
		fmt.Fprintf(&b, "  calibration >= 0.8   %s inside the claimed radius (n=%d, target >= %.0f%%): %s\n",
			pct(s.Calibration), s.ConfidentN, TargetCalibration*100, status(*s.Calibration >= TargetCalibration))
	} else {
		b.WriteString("  calibration >= 0.8   no confident answers to judge\n")
	}
	if s.FalseConfidence != nil {
		fmt.Fprintf(&b, "  false confidence     %s of confident answers over 500 m off (target <= %.0f%%): %s\n",
			pct(s.FalseConfidence), TargetFalseConfidence*100, status(*s.FalseConfidence <= TargetFalseConfidence))
	}
	if len(s.Worst) > 0 {
		b.WriteString("\n  largest errors:\n")
		for _, w := range s.Worst {
			fmt.Fprintf(&b, "    %7.0f m  %-8s conf %.2f  %s\n", w.ErrorM, w.Precision, w.Conf, w.Raw)
		}
	}
	if s.N < 30 {
		fmt.Fprintf(&b, "\n  note: only %d cases; percentages move a lot with so few (60 gives about +/-11 points)\n", s.N)
	}
	return b.String()
}
