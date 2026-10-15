package gazetteer

import (
	"context"
	"database/sql"
	"math"
	"sort"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
)

// Source answers address components from the PostGIS gazetteer. It implements resolver.Source.
//
// Scoring is deliberately simple and explained in the comments below. Every number here is a
// provisional weight that the E1-lite evaluation will calibrate; the structure is what matters:
// confidence is name similarity, discounted for lack of corroborating context, for ambiguity
// (other plausible places with the same name) and, for roads, for how long the road is.
type Source struct {
	DB   *sql.DB
	Norm *resolver.Normalizer
}

// NewSource builds a Source.
func NewSource(db *sql.DB, n *resolver.Normalizer) *Source { return &Source{DB: db, Norm: n} }

// Name implements resolver.Source.
func (s *Source) Name() string { return "gazetteer" }

type point struct{ lat, lng float64 }

type hit struct {
	id      int64
	name    string
	sim     float64
	at      point // representative point, or the closest point on a road to the context
	extentM float64
	lgaID   sql.NullInt64
	ctxDist sql.NullFloat64 // metres from the feature to the context point, when there is one
}

// Resolve implements resolver.Source.
func (s *Source) Resolve(ctx context.Context, c resolver.Components) ([]resolver.Candidate, error) {
	var out []resolver.Candidate
	add := func(prec resolver.Precision, at point, conf float64, ambiguous bool, matched string) {
		if conf <= 0 {
			return
		}
		out = append(out, resolver.Candidate{Lat: at.lat, Lng: at.lng, Precision: prec, Confidence: round2(conf),
			Source: "gazetteer", Matched: []string{matched}, Ambiguous: ambiguous})
	}

	stateID, statePt, err := s.state(ctx, c.State)
	if err != nil {
		return nil, err
	}
	if stateID != 0 {
		add(resolver.State, statePt, 0.9, false, "state")
	}

	// Unclassified place names: the first is usually the neighbourhood, but a misspelt road word
	// ("allen avenu") also lands here, so every one is tried as a locality and, if there is no
	// street yet, as a road.
	var texts []string
	for _, t := range append([]string{c.Area}, c.Other...) {
		if t != "" {
			texts = append(texts, t)
		}
	}

	// Locality: the place name that gives the rest of the address a neighbourhood to be in.
	var loc *point
	locText := ""
	var locBest hit
	locConf, locAmb := 0.0, false
	for _, text := range texts {
		hits, err := s.find(ctx, query{key: s.Norm.NameKey(text), kind: "locality", state: stateID, minSim: 0.45, limit: 10})
		if err != nil {
			return nil, err
		}
		if best, conf, rivals, ok := choose(hits, false); ok && conf > locConf {
			locBest, locConf, locAmb, locText = best, conf, rivals > 0, text
		}
	}
	if locText != "" {
		add(resolver.Locality, locBest.at, locConf, locAmb, "area")
		p := locBest.at
		loc = &p
	}
	// The area text may name an LGA instead (Ikeja is both).
	if c.LGA == "" {
		for _, text := range texts {
			lh, err := s.find(ctx, query{key: s.Norm.NameKey(text), kind: "lga", state: stateID, minSim: 0.6, limit: 3})
			if err != nil {
				return nil, err
			}
			if best, conf, rivals, ok := choose(lh, false); ok {
				add(resolver.LGA, best.at, 0.55*conf, rivals > 0, "area")
				break
			}
		}
	}
	if c.LGA != "" {
		hits, err := s.find(ctx, query{key: s.Norm.NameKey(c.LGA), kind: "lga", state: stateID, minSim: 0.5, limit: 3})
		if err != nil {
			return nil, err
		}
		if best, conf, rivals, ok := choose(hits, false); ok {
			add(resolver.LGA, best.at, 0.6*conf, rivals > 0, "lga")
		}
	}

	// Street: confidence also falls with road length, because a "street" answer claims a 300 m radius
	// and a long road cannot honestly promise that from one point.
	var streetPt *point
	streetTexts := []string{}
	if c.Street != "" {
		streetTexts = append(streetTexts, c.Street)
	} else {
		for _, t := range texts {
			if t != locText {
				streetTexts = append(streetTexts, t)
			}
		}
	}
	for si, streetText := range streetTexts {
		if si > 0 && streetPt != nil {
			break
		}
		key := s.Norm.NameKey(streetText)
		minSim := 0.5
		if c.Street == "" {
			minSim = 0.6 // a guess from an unclassified word needs a closer match
		}
		hits, err := s.find(ctx, query{key: key, kind: "road", state: stateID, minSim: minSim, limit: 60, ctx: loc})
		if err != nil {
			return nil, err
		}
		if best, conf, rivals, ok := choose(hits, loc != nil); ok {
			extent := roadExtent(hits, best, loc != nil)
			add(resolver.Street, best.at, conf*extentFactor(extent), rivals > 0, "street")
			p := best.at
			streetPt = &p
		}
		if c.HouseNumber != "" {
			if ap, found, many, err := s.address(ctx, c.HouseNumber, key, stateID, firstPoint(streetPt, loc)); err != nil {
				return nil, err
			} else if found {
				f := 0.95
				if many {
					f = 0.65
				}
				add(resolver.Building, ap, f, many, "house_number")
			}
		}
	}

	// Landmarks: the named place nearest the street or neighbourhood the address already gave.
	around := firstPoint(streetPt, loc)
	for _, l := range c.Landmarks {
		hits, err := s.find(ctx, query{key: s.Norm.NameKey(l.Name), kind: "place", state: stateID, minSim: 0.5, limit: 40, ctx: around})
		if err != nil {
			return nil, err
		}
		best, conf, rivals, ok := choose(hits, around != nil)
		if !ok {
			continue
		}
		if l.Relation != "" {
			// "opposite First Bank" puts the address near the bank, not on it: street-class at best.
			add(resolver.Street, best.at, math.Min(conf, 0.8), rivals > 0, "landmark:"+l.Name)
		} else {
			add(resolver.Building, best.at, math.Min(conf, 0.9), rivals > 0, "landmark:"+l.Name)
		}
	}
	return out, nil
}

// ---- queries

type query struct {
	key    string
	kind   string
	state  int64
	minSim float64
	limit  int
	ctx    *point // when set, roads snap to the closest point to it and every hit gets a distance
}

func (s *Source) find(ctx context.Context, q query) ([]hit, error) {
	var stateArg any
	if q.state != 0 {
		stateArg = q.state
	}
	var lat, lng any
	if q.ctx != nil {
		lat, lng = q.ctx.lat, q.ctx.lng
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT n.feature_id, n.name, n.similarity::float8,
		       ST_Y(CASE WHEN $6::float8 IS NOT NULL AND f.kind = 'road'
		                 THEN ST_ClosestPoint(f.geom, ST_SetSRID(ST_MakePoint($7::float8, $6::float8), 4326)) ELSE f.rep_point END),
		       ST_X(CASE WHEN $6::float8 IS NOT NULL AND f.kind = 'road'
		                 THEN ST_ClosestPoint(f.geom, ST_SetSRID(ST_MakePoint($7::float8, $6::float8), 4326)) ELSE f.rep_point END),
		       CASE WHEN f.kind = 'road' THEN ST_Length(f.geom::geography) ELSE 0 END,
		       f.lga_id,
		       CASE WHEN $6::float8 IS NULL THEN NULL
		            ELSE ST_Distance(f.geom::geography, ST_SetSRID(ST_MakePoint($7::float8, $6::float8), 4326)::geography) END
		FROM gaz.search_names($1, $2::gaz.feature_kind, $3::bigint, $4::real, $5::integer) n
		JOIN gaz.current_features f ON f.id = n.feature_id`,
		q.key, q.kind, stateArg, q.minSim, q.limit, lat, lng)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	best := map[int64]hit{} // a feature can match under several of its names: keep the best
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.name, &h.sim, &h.at.lat, &h.at.lng, &h.extentM, &h.lgaID, &h.ctxDist); err != nil {
			return nil, err
		}
		if old, ok := best[h.id]; !ok || h.sim > old.sim {
			best[h.id] = h
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]hit, 0, len(best))
	for _, h := range best {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].sim != out[j].sim {
			return out[i].sim > out[j].sim
		}
		return out[i].id < out[j].id
	})
	return out, nil
}

func (s *Source) state(ctx context.Context, name string) (int64, point, error) {
	if name == "" {
		return 0, point{}, nil
	}
	var id int64
	var p point
	err := s.DB.QueryRowContext(ctx, `
		SELECT f.id, ST_Y(f.rep_point), ST_X(f.rep_point) FROM gaz.current_names n
		JOIN gaz.current_features f ON f.id = n.feature_id
		WHERE n.kind = 'state' AND n.name_norm = $1 LIMIT 1`, s.Norm.NameKey(name)).Scan(&id, &p.lat, &p.lng)
	if err == sql.ErrNoRows {
		return 0, point{}, nil
	}
	return id, p, err
}

// address finds an address point with this house number on this street, near the context if any.
func (s *Source) address(ctx context.Context, number, streetKey string, state int64, near *point) (point, bool, bool, error) {
	var stateArg, lat, lng any
	if state != 0 {
		stateArg = state
	}
	if near != nil {
		lat, lng = near.lat, near.lng
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT ST_Y(f.rep_point), ST_X(f.rep_point) FROM gaz.current_features f
		WHERE f.kind = 'address' AND f.house_number = $1 AND f.street_norm = $2
		  AND ($3::bigint IS NULL OR f.state_id = $3::bigint)
		  AND ($4::float8 IS NULL OR ST_Distance(f.rep_point::geography, ST_SetSRID(ST_MakePoint($5::float8, $4::float8), 4326)::geography) <= 3000)
		LIMIT 5`, number, streetKey, stateArg, lat, lng)
	if err != nil {
		return point{}, false, false, err
	}
	defer rows.Close()
	var pts []point
	for rows.Next() {
		var p point
		if err := rows.Scan(&p.lat, &p.lng); err != nil {
			return point{}, false, false, err
		}
		pts = append(pts, p)
	}
	if len(pts) == 0 {
		return point{}, false, false, rows.Err()
	}
	return pts[0], true, len(pts) > 1, rows.Err()
}

// ---- scoring

// choose picks the best hit and reports how many other plausible places share the name.
// A hit's score is its name similarity times how well it fits the context point (if there is one).
func choose(hits []hit, hasCtx bool) (best hit, conf float64, rivals int, ok bool) {
	if len(hits) == 0 {
		return hit{}, 0, 0, false
	}
	type sc struct {
		h hit
		s float64
	}
	scored := make([]sc, len(hits))
	for i, h := range hits {
		scored[i] = sc{h, h.sim * contextFactor(h.ctxDist, hasCtx)}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].s > scored[j].s })
	top := scored[0]
	for _, o := range scored[1:] {
		// a rival is a different place (over 1.5 km away) that scores nearly as well
		if o.s >= top.s*0.85 && haversine(top.h.at, o.h.at) > 1500 {
			rivals++
		}
	}
	return top.h, top.s * ambiguityFactor(rivals), rivals, true
}

// contextFactor: how much does the context point corroborate this place?
func contextFactor(d sql.NullFloat64, hasCtx bool) float64 {
	if !hasCtx || !d.Valid {
		return 0.9 // nothing to confirm it against
	}
	switch {
	case d.Float64 <= 1000:
		return 1.0
	case d.Float64 <= 3000:
		return 0.85
	case d.Float64 <= 8000:
		return 0.5
	}
	return 0.2
}

func ambiguityFactor(rivals int) float64 {
	switch {
	case rivals == 0:
		return 1.0
	case rivals == 1:
		return 0.75
	case rivals <= 4:
		return 0.55
	}
	return 0.4
}

// roadExtent is the length of the road with this name near the context (all its segments).
func roadExtent(hits []hit, best hit, hasCtx bool) float64 {
	total := 0.0
	for _, h := range hits {
		if h.sim < best.sim-0.05 {
			continue
		}
		if hasCtx && h.ctxDist.Valid && h.ctxDist.Float64 > 3000 {
			continue
		}
		total += h.extentM
	}
	if total < best.extentM {
		total = best.extentM
	}
	return total
}

func extentFactor(m float64) float64 {
	switch {
	case m <= 600:
		return 1.0
	case m <= 1500:
		return 0.8
	case m <= 4000:
		return 0.6
	}
	return 0.4
}

func firstPoint(ps ...*point) *point {
	for _, p := range ps {
		if p != nil {
			return p
		}
	}
	return nil
}

func haversine(a, b point) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dLat, dLng := (b.lat-a.lat)*rad, (b.lng-a.lng)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(a.lat*rad)*math.Cos(b.lat*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
