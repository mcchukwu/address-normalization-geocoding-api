package resolver

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Source offers candidate locations for a normalized address. The local gazetteer, the
// NDAPS adapter and the Google adapter all implement it, so each can be switched off
// without touching the rest of the pipeline.
type Source interface {
	Name() string
	Resolve(ctx context.Context, c Components) ([]Candidate, error)
}

// Thresholds are the confidence each precision class must reach to be claimed.
// They are placeholders until the E1-lite set has been used to calibrate them.
type Thresholds map[Precision]float64

// DefaultThresholds returns provisional values.
func DefaultThresholds() Thresholds {
	return Thresholds{Building: 0.85, Street: 0.70, Locality: 0.50, LGA: 0.40, State: 0.40}
}

// Pipeline is: normalize, ask every source, then decide which precision may be claimed.
type Pipeline struct {
	Norm          *Normalizer
	Sources       []Source
	Thresholds    Thresholds
	SourceTimeout time.Duration
}

// NewPipeline builds a pipeline with default thresholds and an 800 ms source timeout.
func NewPipeline(n *Normalizer, sources ...Source) *Pipeline {
	return &Pipeline{Norm: n, Sources: sources, Thresholds: DefaultThresholds(), SourceTimeout: 800 * time.Millisecond}
}

// Normalize exposes the first stage on its own.
func (p *Pipeline) Normalize(raw string, h Hints) (Components, []string) {
	return p.Norm.Normalize(raw, h)
}

// Geocode runs every stage. It never returns an error: a failing source becomes a warning,
// and an unresolvable address becomes an Answer with no Fix.
func (p *Pipeline) Geocode(ctx context.Context, raw string, h Hints, maxCandidates int) Answer {
	comps, warnings := p.Norm.Normalize(raw, h)
	ans := Answer{Normalized: comps, Warnings: warnings, Candidates: []Candidate{}, SourcesUsed: []string{}, SourcesFailed: []string{}}

	type outcome struct {
		name  string
		cands []Candidate
		err   error
	}
	results := make([]outcome, len(p.Sources))
	var wg sync.WaitGroup
	for i, s := range p.Sources {
		wg.Add(1)
		go func(i int, s Source) {
			defer wg.Done()
			// A misbehaving source must never take the whole service down.
			defer func() {
				if r := recover(); r != nil {
					results[i] = outcome{s.Name(), nil, fmt.Errorf("source panicked: %v", r)}
				}
			}()
			sctx, cancel := context.WithTimeout(ctx, p.SourceTimeout)
			defer cancel()
			cands, err := s.Resolve(sctx, comps)
			if err == nil && sctx.Err() != nil {
				err = sctx.Err()
			}
			results[i] = outcome{s.Name(), cands, err}
		}(i, s)
	}
	wg.Wait()

	var all []Candidate
	for _, r := range results {
		if r.err != nil {
			ans.Warnings = appendOnce(ans.Warnings, "source_unavailable")
			ans.SourcesFailed = append(ans.SourcesFailed, r.name)
			continue
		}
		ans.SourcesUsed = append(ans.SourcesUsed, r.name)
		all = append(all, r.cands...)
	}

	// Finest class first; within a class, most confident first.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Precision.Rank() != all[j].Precision.Rank() {
			return all[i].Precision.Rank() < all[j].Precision.Rank()
		}
		return all[i].Confidence > all[j].Confidence
	})

	// Decide precision: the finest candidate whose confidence clears its class's bar, without
	// answering a different question from the one the caller asked.
	var evidenceWarnings []string
	ans.Fix, evidenceWarnings, ans.Unmatched = p.decide(comps, all)
	for _, w := range evidenceWarnings {
		ans.Warnings = appendOnce(ans.Warnings, w)
	}
	if ans.Fix == nil && len(all) > 0 {
		ans.Warnings = appendOnce(ans.Warnings, "low_confidence")
	}

	if maxCandidates > 0 && len(all) > maxCandidates {
		all = all[:maxCandidates]
	}
	if all != nil {
		ans.Candidates = all
	}
	return ans
}

// evidence is what the caller supplied and what the candidates actually matched. It is the
// basis for refusing to answer a different question from the one asked.
type evidence struct {
	unmatched            []string
	kinds                map[string]bool
	primaryAreaUnmatched bool
	matched              map[string]bool // "kind|name" for matches at threshold
}

func (p *Pipeline) decide(c Components, cands []Candidate) (*Fix, []string, []string) {
	e := p.assess(c, cands)
	warnings := evidenceWarnings(e.kinds)
	for _, cand := range cands {
		if !p.allowed(cand, c, e) {
			continue
		}
		conf := p.capConfidence(cand, e)
		if conf < p.Thresholds[cand.Precision] {
			continue
		}
		if cand.Ambiguous {
			warnings = appendOnce(warnings, "ambiguous_place")
		}
		return &Fix{Lat: cand.Lat, Lng: cand.Lng, Precision: cand.Precision, Confidence: conf}, warnings, e.unmatched
	}
	return nil, warnings, e.unmatched
}

func (p *Pipeline) assess(c Components, cands []Candidate) evidence {
	e := evidence{kinds: map[string]bool{}, matched: map[string]bool{}}
	for _, cand := range cands {
		if cand.Confidence < p.Thresholds[cand.Precision] {
			continue
		}
		for _, label := range cand.Matched {
			e.matched[labelKey(p.Norm, label)] = true
		}
	}
	add := func(kind, text string) bool {
		key := kind + "|" + p.Norm.NameKey(text)
		if e.matched[key] {
			return true
		}
		e.unmatched = append(e.unmatched, kind+":"+text)
		e.kinds[kind] = true
		return false
	}
	if c.Street != "" {
		add("street", c.Street)
	}
	if c.Area != "" {
		e.primaryAreaUnmatched = !add("area", c.Area)
	}
	for _, other := range c.Other {
		add("area", other)
	}
	for _, l := range c.Landmarks {
		add("landmark", l.Name)
	}
	if c.HouseNumber != "" {
		add("house_number", c.HouseNumber)
	}
	if c.LGA != "" {
		add("lga", c.LGA)
	}
	if c.State != "" {
		add("state", c.State)
	}
	return e
}

func (p *Pipeline) allowed(cand Candidate, c Components, e evidence) bool {
	finerUnmatched := e.kinds["street"] || e.kinds["area"] || e.kinds["landmark"] || e.kinds["house_number"]
	switch cand.Precision {
	case LGA:
		if finerUnmatched {
			return false
		}
	case State:
		if finerUnmatched || e.kinds["lga"] {
			return false
		}
	case Locality:
		if e.primaryAreaUnmatched && !p.matchesAny(cand, "area", c.Other) {
			return false
		}
	case Building:
		if e.kinds["house_number"] {
			return false
		}
		if e.kinds["landmark"] && !hasKind(cand, "house_number") {
			return false
		}
	}
	return true
}

func (p *Pipeline) capConfidence(cand Candidate, e evidence) float64 {
	cap := 1.0
	switch cand.Precision {
	case Locality:
		if e.kinds["street"] {
			cap = math.Min(cap, 0.50)
		}
		if e.primaryAreaUnmatched {
			cap = math.Min(cap, 0.50)
		}
		if e.kinds["landmark"] {
			cap = math.Min(cap, 0.60)
		}
	case Street:
		if e.primaryAreaUnmatched {
			cap = math.Min(cap, 0.70)
		}
		if e.kinds["landmark"] {
			cap = math.Min(cap, 0.70)
		}
		if e.kinds["house_number"] {
			cap = math.Min(cap, 0.80)
		}
	}
	return math.Min(cand.Confidence, cap)
}

func (p *Pipeline) matchesAny(cand Candidate, kind string, texts []string) bool {
	for _, text := range texts {
		key := kind + "|" + p.Norm.NameKey(text)
		for _, label := range cand.Matched {
			if labelKey(p.Norm, label) == key {
				return true
			}
		}
	}
	return false
}

func labelKey(n *Normalizer, label string) string {
	if i := strings.IndexByte(label, ':'); i >= 0 {
		return label[:i] + "|" + n.NameKey(label[i+1:])
	}
	return label
}

func hasKind(cand Candidate, kind string) bool {
	for _, label := range cand.Matched {
		if label == kind || strings.HasPrefix(label, kind+":") {
			return true
		}
	}
	return false
}

func evidenceWarnings(kinds map[string]bool) []string {
	var out []string
	for _, kind := range []string{"street", "area", "landmark", "house_number", "lga", "state"} {
		if kinds[kind] {
			out = append(out, kind+"_not_matched")
		}
	}
	return out
}

func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
