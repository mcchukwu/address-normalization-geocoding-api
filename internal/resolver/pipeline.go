package resolver

import (
	"context"
	"fmt"
	"sort"
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

	// Decide precision: the finest candidate whose confidence clears its class's bar.
	for _, c := range all {
		if c.Confidence >= p.Thresholds[c.Precision] {
			ans.Fix = &Fix{Lat: c.Lat, Lng: c.Lng, Precision: c.Precision, Confidence: c.Confidence}
			break
		}
	}
	if ans.Fix == nil && len(all) > 0 {
		ans.Warnings = appendOnce(ans.Warnings, "low_confidence")
	}

	if len(comps.Landmarks) > 0 && !landmarkMatched(all) {
		ans.Warnings = appendOnce(ans.Warnings, "landmark_not_matched")
	}

	if maxCandidates > 0 && len(all) > maxCandidates {
		all = all[:maxCandidates]
	}
	if all != nil {
		ans.Candidates = all
	}
	return ans
}

func landmarkMatched(cands []Candidate) bool {
	for _, c := range cands {
		for _, m := range c.Matched {
			if len(m) > 9 && m[:9] == "landmark:" {
				return true
			}
		}
	}
	return false
}

func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
