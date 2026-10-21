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
	r := &resolution{src: s, ctx: ctx, c: c}
	for _, t := range append([]string{c.Area}, c.Other...) {
		if t != "" {
			r.places = append(r.places, t)
		}
	}
	for _, step := range []func() error{r.addState, r.addLocality, r.addLGA, r.addStreet, r.addLandmarks} {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return r.out, nil
}

// resolution is the state of resolving one address: what has been found so far, which later steps
// use as context (a neighbourhood narrows the street search, a street narrows the landmark search).
type resolution struct {
	src     *Source
	ctx     context.Context
	c       resolver.Components
	stateID int64
	// places are the unclassified names: usually the neighbourhood, but a misspelt road word
	// ("allen avenu") also lands here, so each is tried as a locality and, failing a street, as a road.
	places   []string
	locality *point
	locText  string
	street   *point
	out      []resolver.Candidate
}

func (r *resolution) add(prec resolver.Precision, at point, conf float64, ambiguous bool, matched string) {
	if conf <= 0 {
		return
	}
	r.out = append(r.out, resolver.Candidate{Lat: at.lat, Lng: at.lng, Precision: prec, Confidence: round2(conf),
		Source: "gazetteer", Matched: []string{matched}, Ambiguous: ambiguous})
}

func (r *resolution) find(kind, text string, minSim float64, limit int, around *point) ([]hit, error) {
	return r.src.find(r.ctx, query{key: r.src.Norm.NameKey(text), kind: kind, state: r.stateID, minSim: minSim, limit: limit, ctx: around})
}

func (r *resolution) addState() error {
	id, at, err := r.src.state(r.ctx, r.c.State)
	if err != nil {
		return err
	}
	r.stateID = id
	if id != 0 {
		r.add(resolver.State, at, 0.9, false, "state")
	}
	return nil
}

// addLocality picks the place name that gives the rest of the address a neighbourhood to be in. Names
// are written specific first ("trans amadi, port harcourt"), so the first one that matches reasonably wins;
// a later, more general name (the city) only wins if nothing before it matched.
func (r *resolution) addLocality() error {
	var best hit
	conf, ambiguous := 0.0, false
	for _, text := range r.places {
		hits, err := r.find("locality", text, 0.45, 10, nil)
		if err != nil {
			return err
		}
		h, c, rivals, ok := choose(hits, ctxNone)
		if !ok {
			continue
		}
		if c > conf && (conf == 0 || conf < 0.5) {
			best, conf, ambiguous, r.locText = h, c, rivals > 0, text
		}
		if conf >= 0.5 {
			break
		}
	}
	if r.locText != "" {
		r.add(resolver.Locality, best.at, conf, ambiguous, "area")
		p := best.at
		r.locality = &p
	}
	return nil
}

// addLGA reads an LGA from the text: the named LGA if there is one, or an area name that is also an
// LGA (Ikeja is both).
func (r *resolution) addLGA() error {
	if r.c.LGA != "" {
		hits, err := r.find("lga", r.c.LGA, 0.5, 3, nil)
		if err != nil {
			return err
		}
		if best, conf, rivals, ok := choose(hits, ctxNone); ok {
			r.add(resolver.LGA, best.at, 0.6*conf, rivals > 0, "lga")
		}
		return nil
	}
	for _, text := range r.places {
		hits, err := r.find("lga", text, 0.6, 3, nil)
		if err != nil {
			return err
		}
		if best, conf, rivals, ok := choose(hits, ctxNone); ok {
			r.add(resolver.LGA, best.at, 0.55*conf, rivals > 0, "area")
			return nil
		}
	}
	return nil
}

// streetTexts are the names to try as a road: the parsed street, or else any place name that is not
// the neighbourhood.
func (r *resolution) streetTexts() []string {
	if r.c.Street != "" {
		return []string{r.c.Street}
	}
	var out []string
	for _, t := range r.places {
		if t != r.locText {
			out = append(out, t)
		}
	}
	return out
}

func (r *resolution) addStreet() error {
	minSim := 0.5
	if r.c.Street == "" {
		minSim = 0.6 // a guess from an unclassified word needs a closer match
	}
	for i, text := range r.streetTexts() {
		if i > 0 && r.street != nil {
			break
		}
		if err := r.addRoad(text, minSim); err != nil {
			return err
		}
		if r.c.HouseNumber != "" {
			if err := r.addAddress(text); err != nil {
				return err
			}
		}
	}
	return nil
}

// addRoad adds a street-class candidate. Confidence also falls with road length: a "street" answer
// claims a 300 m radius, which a long road cannot honestly promise from one point.
func (r *resolution) addRoad(text string, minSim float64) error {
	hits, err := r.find("road", text, minSim, 60, r.locality)
	if err != nil {
		return err
	}
	best, conf, rivals, ok := choose(hits, kindOf(r.locality, nil))
	if !ok {
		return nil
	}
	extent := roadExtent(hits, best, r.locality != nil)
	r.add(resolver.Street, best.at, conf*extentFactor(extent), rivals > 0, "street")
	p := best.at
	r.street = &p
	return nil
}

// addAddress adds a building-class candidate when an address point with this house number exists on the street.
func (r *resolution) addAddress(streetText string) error {
	at, found, many, err := r.src.address(r.ctx, r.c.HouseNumber, r.src.Norm.NameKey(streetText), r.stateID, firstPoint(r.street, r.locality))
	if err != nil || !found {
		return err
	}
	conf := 0.95
	if many {
		conf = 0.65
	}
	r.add(resolver.Building, at, conf, many, "house_number")
	return nil
}

// addLandmarks looks for each landmark nearest the street or neighbourhood the address already gave.
func (r *resolution) addLandmarks() error {
	around := firstPoint(r.street, r.locality)
	kind := kindOf(around, r.street)
	for _, l := range r.c.Landmarks {
		hits, err := r.find("place", l.Name, 0.5, 40, around)
		if err != nil {
			return err
		}
		best, conf, rivals, ok := choose(hits, kind)
		if !ok {
			continue
		}
		if l.Relation == "" {
			r.add(resolver.Building, best.at, math.Min(conf, 0.9), rivals > 0, "landmark:"+l.Name)
			continue
		}
		// "opposite First Bank" puts the address near the bank, not on it: street-class at best, and only
		// when the bank was checked against the street. Against a neighbourhood alone it cannot be claimed.
		limit := 0.8
		if kind != ctxStreet {
			limit = 0.6
		}
		r.add(resolver.Street, best.at, math.Min(conf, limit), rivals > 0, "landmark:"+l.Name)
	}
	return r.addNamedPlaces(around, kind)
}

// addNamedPlaces tries the unclassified names as places too: "computer village, ikeja" names a market, but
// the parser cannot know that "computer village" is not a neighbourhood.
func (r *resolution) addNamedPlaces(around *point, kind ctxKind) error {
	for _, text := range r.places {
		if text == r.locText {
			continue
		}
		hits, err := r.find("place", text, 0.7, 20, around)
		if err != nil {
			return err
		}
		if best, conf, rivals, ok := choose(hits, kind); ok && conf >= 0.5 {
			r.add(resolver.Street, best.at, math.Min(conf, 0.8), rivals > 0, "place:"+text)
		}
	}
	return nil
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

// ctxKind says what the context point is, because that decides how close a place must be to corroborate it.
type ctxKind int

const (
	ctxNone     ctxKind = iota // nothing to confirm against
	ctxLocality                // the centre of a neighbourhood
	ctxStreet                  // a point on the named road
)

func kindOf(around, street *point) ctxKind {
	switch {
	case street != nil:
		return ctxStreet
	case around != nil:
		return ctxLocality
	}
	return ctxNone
}

// choose picks the best hit and reports how many other plausible places share the name.
// A hit's score is its name similarity times how well it fits the context point (if there is one).
func choose(hits []hit, kind ctxKind) (best hit, conf float64, rivals int, ok bool) {
	if len(hits) == 0 {
		return hit{}, 0, 0, false
	}
	type sc struct {
		h hit
		s float64
	}
	scored := make([]sc, len(hits))
	for i, h := range hits {
		scored[i] = sc{h, h.sim * contextFactor(h.ctxDist, kind)}
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

// contextFactor: how much does the context point corroborate this place? Against a point on the named
// road a landmark must be close (an address "opposite First Bank" is within a few hundred metres of the
// bank), so a bank 3 km along the road is a different bank. Against a neighbourhood centre, which is
// itself kilometres wide, the bands are looser.
func contextFactor(d sql.NullFloat64, kind ctxKind) float64 {
	if kind == ctxNone || !d.Valid {
		return 0.9 // nothing to confirm it against
	}
	m := d.Float64
	if kind == ctxStreet {
		switch {
		case m <= 400:
			return 1.0
		case m <= 800:
			return 0.8
		case m <= 1500:
			return 0.4
		}
		return 0.1
	}
	switch {
	case m <= 1000:
		return 1.0
	case m <= 3000:
		return 0.85
	case m <= 8000:
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
