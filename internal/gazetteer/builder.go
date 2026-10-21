// Package gazetteer builds the searchable place index (gaz.*) from open data and answers
// queries against it. Boundaries come from geoBoundaries GeoJSON; roads, places and addresses
// come from an OpenStreetMap extract that osm2pgsql has already staged into osm_stage.
package gazetteer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
)

// Options controls one gazetteer build.
type Options struct {
	Label    string // e.g. 2026-10-gaz-1; every API answer carries it
	ADM1Path string // geoBoundaries NGA ADM1 GeoJSON (states)
	ADM2Path string // geoBoundaries NGA ADM2 GeoJSON (LGAs)
	Norm     *resolver.Normalizer
	Activate bool // make this build the one the API reads
	Notes    string
	Log      *slog.Logger
}

// Report says what a build produced.
type Report struct {
	VersionID  int
	Label      string
	Counts     map[string]int // by feature kind
	Names      int
	Activated  bool
	Unassigned int // features that fell outside every LGA
	Elapsed    time.Duration
}

type featureCollection struct {
	Features []struct {
		Properties struct {
			ShapeName string `json:"shapeName"`
			ShapeID   string `json:"shapeID"`
		} `json:"properties"`
		Geometry json.RawMessage `json:"geometry"`
	} `json:"features"`
}

type nameRow struct {
	feature int64
	name    string
}

// Build creates a new gazetteer version in one transaction. If anything fails, nothing is kept.
func Build(ctx context.Context, db *sql.DB, o Options) (Report, error) {
	start := time.Now()
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	r := Report{Label: o.Label, Counts: map[string]int{}}
	if err := requireStaging(ctx, db); err != nil {
		return r, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()

	if err := tx.QueryRowContext(ctx,
		`INSERT INTO gaz.data_versions (label, boundaries_source, notes) VALUES ($1, 'geoBoundaries (CC BY 4.0)', $2) RETURNING id`,
		o.Label, nullable(o.Notes)).Scan(&r.VersionID); err != nil {
		return r, fmt.Errorf("create data version %q: %w", o.Label, err)
	}
	v := r.VersionID

	steps := []struct {
		name string
		run  func() error
	}{
		{"boundaries loaded", func() error { return loadBoundaries(ctx, tx, o, v) }},
		{"osm features loaded", func() error { return stageFeatures(ctx, tx, o.Norm, v) }},
		{"spatial assignment done", func() error { return assignAreas(ctx, tx, v) }},
		{"names loaded", func() error { return loadNames(ctx, tx, o.Norm, v) }},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			return r, err
		}
		o.Log.Info(step.name)
	}
	if err := summarise(ctx, tx, v, &r); err != nil {
		return r, err
	}
	if o.Activate {
		if _, err := tx.ExecContext(ctx, `INSERT INTO gaz.active_version (singleton, data_version_id) VALUES (true, $1)
			ON CONFLICT (singleton) DO UPDATE SET data_version_id = EXCLUDED.data_version_id`, v); err != nil {
			return r, err
		}
		r.Activated = true
	}
	if err := tx.Commit(); err != nil {
		return r, err
	}
	for _, t := range []string{"gaz.features", "gaz.names"} {
		if _, err := db.ExecContext(ctx, `ANALYZE `+t); err != nil {
			o.Log.Warn("analyze failed", "table", t, "err", err.Error())
		}
	}
	r.Elapsed = time.Since(start)
	return r, nil
}

func requireStaging(ctx context.Context, db *sql.DB) error {
	for _, t := range []string{"osm_stage.roads", "osm_stage.places", "osm_stage.addresses"} {
		var reg sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, t).Scan(&reg); err != nil || !reg.Valid {
			return fmt.Errorf("%s not found: run osm2pgsql with scripts/osm2pgsql-gazetteer.lua first", t)
		}
	}
	return nil
}

// stageSQL copies the osm2pgsql staging tables into gaz.features, one statement per kind of feature.
var stageSQL = []struct{ name, sql string }{
	{"roads", `INSERT INTO gaz.features (data_version_id, kind, category, geom, source, source_id, licence, fetched_at)
		SELECT $1, 'road', r.highway, r.geom, 'osm', 'way/' || r.osm_id, 'ODbL', now()
		FROM osm_stage.roads r WHERE r.geom IS NOT NULL AND NOT ST_IsEmpty(r.geom)`},
	{"places", `INSERT INTO gaz.features (data_version_id, kind, category, geom, source, source_id, licence, fetched_at)
		SELECT $1,
		  CASE WHEN p.place IN ('city','town','village','suburb','neighbourhood','quarter','hamlet','locality','borough')
		       THEN 'locality'::gaz.feature_kind ELSE 'place'::gaz.feature_kind END,
		  COALESCE(p.category, p.place),
		  CASE WHEN GeometryType(p.geom) IN ('POLYGON','MULTIPOLYGON') THEN ST_MakeValid(p.geom) ELSE p.geom END,
		  'osm', CASE p.osm_type WHEN 'N' THEN 'node' WHEN 'W' THEN 'way' ELSE 'relation' END || '/' || p.osm_id,
		  'ODbL', now()
		FROM osm_stage.places p WHERE p.geom IS NOT NULL AND NOT ST_IsEmpty(p.geom)`},
	{"addresses", `INSERT INTO gaz.features (data_version_id, kind, geom, house_number, street_norm, source, source_id, licence, fetched_at)
		SELECT $1, 'address', ST_PointOnSurface(a.geom), lower(btrim(a.housenumber)), a.street,
		  'osm', CASE a.osm_type WHEN 'N' THEN 'node' WHEN 'W' THEN 'way' ELSE 'relation' END || '/' || a.osm_id,
		  'ODbL', now()
		FROM osm_stage.addresses a WHERE a.housenumber IS NOT NULL AND a.geom IS NOT NULL AND NOT ST_IsEmpty(a.geom)`},
}

func stageFeatures(ctx context.Context, tx *sql.Tx, norm *resolver.Normalizer, v int) error {
	for _, s := range stageSQL {
		if _, err := tx.ExecContext(ctx, s.sql, v); err != nil {
			return fmt.Errorf("stage %s into gaz.features: %w", s.name, err)
		}
	}
	return normaliseStreets(ctx, tx, norm, v)
}

// areaJoinSQL says which LGA and state each feature is in, by spatial join on its representative point.
var areaJoinSQL = []string{
	`UPDATE gaz.features l SET state_id = s.id, parent_id = s.id FROM gaz.features s
	   WHERE l.data_version_id = $1 AND s.data_version_id = $1 AND l.kind = 'lga' AND s.kind = 'state'
	     AND ST_Intersects(s.geom, l.rep_point)`,
	`UPDATE gaz.features f SET lga_id = l.id, state_id = l.state_id FROM gaz.features l
	   WHERE f.data_version_id = $1 AND l.data_version_id = $1 AND l.kind = 'lga'
	     AND f.kind IN ('locality','road','place','address') AND ST_Contains(l.geom, f.rep_point)`,
	`UPDATE gaz.features f SET state_id = s.id FROM gaz.features s
	   WHERE f.data_version_id = $1 AND s.data_version_id = $1 AND s.kind = 'state'
	     AND f.kind IN ('locality','road','place','address') AND f.state_id IS NULL AND ST_Contains(s.geom, f.rep_point)`,
}

func assignAreas(ctx context.Context, tx *sql.Tx, v int) error {
	if _, err := tx.ExecContext(ctx, `ANALYZE gaz.features`); err != nil {
		return err
	}
	for _, q := range areaJoinSQL {
		if _, err := tx.ExecContext(ctx, q, v); err != nil {
			return fmt.Errorf("spatial assignment: %w", err)
		}
	}
	return nil
}

// namesSQL reads every staged name with the feature it belongs to.
var namesSQL = []string{
	`SELECT f.id, concat_ws(';', s.name, s.alt_names) FROM osm_stage.roads s
	   JOIN gaz.features f ON f.data_version_id = $1 AND f.source = 'osm' AND f.kind = 'road' AND f.source_id = 'way/' || s.osm_id`,
	`SELECT f.id, concat_ws(';', s.name, s.alt_names) FROM osm_stage.places s
	   JOIN gaz.features f ON f.data_version_id = $1 AND f.source = 'osm' AND f.kind IN ('locality','place')
	    AND f.source_id = CASE s.osm_type WHEN 'N' THEN 'node' WHEN 'W' THEN 'way' ELSE 'relation' END || '/' || s.osm_id`,
}

// loadNames normalises every staged name in Go (the single normaliser) and bulk-copies gaz.names.
func loadNames(ctx context.Context, tx *sql.Tx, norm *resolver.Normalizer, v int) error {
	var rows []nameRow
	for _, q := range namesSQL {
		got, err := queryNames(ctx, tx, q, v)
		if err != nil {
			return err
		}
		rows = append(rows, got...)
	}
	_, err := copyNames(ctx, tx, norm, rows)
	return err
}

// summarise fills the report from what the build produced.
func summarise(ctx context.Context, tx *sql.Tx, v int, r *Report) error {
	rs, err := tx.QueryContext(ctx, `SELECT kind::text, count(*) FROM gaz.features WHERE data_version_id = $1 GROUP BY kind`, v)
	if err != nil {
		return err
	}
	for rs.Next() {
		var k string
		var c int
		if err := rs.Scan(&k, &c); err != nil {
			rs.Close()
			return err
		}
		r.Counts[k] = c
	}
	rs.Close()
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM gaz.names n JOIN gaz.features f ON f.id = n.feature_id WHERE f.data_version_id = $1`, v).Scan(&r.Names); err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, `SELECT count(*) FROM gaz.features WHERE data_version_id = $1 AND kind IN ('locality','road','place','address') AND lga_id IS NULL`, v).Scan(&r.Unassigned)
}

func loadBoundaries(ctx context.Context, tx *sql.Tx, o Options, v int) error {
	for _, level := range []struct{ kind, path string }{{"state", o.ADM1Path}, {"lga", o.ADM2Path}} {
		raw, err := os.ReadFile(level.path)
		if err != nil {
			return fmt.Errorf("read %s boundaries: %w", level.kind, err)
		}
		var fc featureCollection
		if err := json.Unmarshal(raw, &fc); err != nil {
			return fmt.Errorf("parse %s: %w", level.path, err)
		}
		if len(fc.Features) == 0 {
			return fmt.Errorf("%s has no features", level.path)
		}
		var unmatched []string
		for i, f := range fc.Features {
			name := strings.TrimSpace(f.Properties.ShapeName)
			sourceID := f.Properties.ShapeID
			if sourceID == "" {
				sourceID = fmt.Sprintf("%s-%d", level.kind, i)
			}
			names := []string{name}
			if level.kind == "state" {
				si, ok := o.Norm.StateByName(name)
				if !ok {
					unmatched = append(unmatched, name)
					continue
				}
				names = append([]string{si.Name}, si.Aliases...)
				if si.Name != name {
					names = append(names, name)
				}
			}
			var id int64
			err := tx.QueryRowContext(ctx, `INSERT INTO gaz.features (data_version_id, kind, geom, source, source_id, licence, fetched_at)
				VALUES ($1, $2::gaz.feature_kind,
				  ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_SetSRID(ST_GeomFromGeoJSON($3), 4326)), 3)),
				  'geoboundaries', $4, 'CC BY 4.0', now()) RETURNING id`,
				v, level.kind, string(f.Geometry), sourceID).Scan(&id)
			if err != nil {
				return fmt.Errorf("insert %s %q: %w", level.kind, name, err)
			}
			if err := insertNames(ctx, tx, o.Norm, id, names); err != nil {
				return err
			}
		}
		if len(unmatched) > 0 {
			sort.Strings(unmatched)
			return fmt.Errorf("state names not recognised: %s (add them as aliases in ref.states and the Go dictionary)", strings.Join(unmatched, ", "))
		}
	}
	return nil
}

func insertNames(ctx context.Context, tx *sql.Tx, norm *resolver.Normalizer, id int64, names []string) error {
	seen := map[string]bool{}
	for i, n := range names {
		key := norm.NameKey(n)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO gaz.names (feature_id, name, name_norm, is_primary) VALUES ($1, $2, $3, $4)`,
			id, n, key, i == 0); err != nil {
			return err
		}
	}
	return nil
}

func queryNames(ctx context.Context, tx *sql.Tx, q string, v int) ([]nameRow, error) {
	rs, err := tx.QueryContext(ctx, q, v)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []nameRow
	for rs.Next() {
		var id int64
		var joined string
		if err := rs.Scan(&id, &joined); err != nil {
			return nil, err
		}
		out = append(out, nameRow{id, joined})
	}
	return out, rs.Err()
}

// copyNames normalises every name and bulk-loads gaz.names. The first name of a feature is primary.
func copyNames(ctx context.Context, tx *sql.Tx, norm *resolver.Normalizer, rows []nameRow) (int, error) {
	stmt, err := tx.PrepareContext(ctx, pq.CopyInSchema("gaz", "names", "feature_id", "name", "name_norm", "is_primary"))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, row := range rows {
		seen := map[string]bool{}
		first := true
		for _, name := range strings.Split(row.name, ";") {
			name = strings.TrimSpace(name)
			key := norm.NameKey(name)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			if _, err := stmt.ExecContext(ctx, row.feature, name, key, first); err != nil {
				stmt.Close()
				return n, err
			}
			first = false
			n++
		}
	}
	if _, err := stmt.ExecContext(ctx); err != nil {
		stmt.Close()
		return n, err
	}
	return n, stmt.Close()
}

// normaliseStreets rewrites the raw street text of address points into name keys.
func normaliseStreets(ctx context.Context, tx *sql.Tx, norm *resolver.Normalizer, v int) error {
	rs, err := tx.QueryContext(ctx, `SELECT DISTINCT street_norm FROM gaz.features WHERE data_version_id = $1 AND kind = 'address' AND street_norm IS NOT NULL`, v)
	if err != nil {
		return err
	}
	var raws []string
	for rs.Next() {
		var s string
		if err := rs.Scan(&s); err != nil {
			rs.Close()
			return err
		}
		raws = append(raws, s)
	}
	rs.Close()
	for _, raw := range raws {
		key := norm.NameKey(raw)
		if key == raw {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gaz.features SET street_norm = NULLIF($3, '') WHERE data_version_id = $1 AND kind = 'address' AND street_norm = $2`, v, raw, key); err != nil {
			return err
		}
	}
	return nil
}

// ActiveVersion reports which build the API is reading.
func ActiveVersion(ctx context.Context, db *sql.DB) (id int, label string, err error) {
	err = db.QueryRowContext(ctx, `SELECT d.id, d.label FROM gaz.active_version a JOIN gaz.data_versions d ON d.id = a.data_version_id`).Scan(&id, &label)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", errors.New("no active gazetteer version: run the gazetteer builder with -activate")
	}
	return
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
