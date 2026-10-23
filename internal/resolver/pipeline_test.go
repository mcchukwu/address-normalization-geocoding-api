package resolver

import (
	"context"
	"errors"
	"testing"
	"time"
)

type slowSource struct{}

func (slowSource) Name() string { return "slow" }
func (slowSource) Resolve(ctx context.Context, _ Components) ([]Candidate, error) {
	select {
	case <-time.After(2 * time.Second):
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type brokenSource struct{}

func (brokenSource) Name() string { return "broken" }
func (brokenSource) Resolve(context.Context, Components) ([]Candidate, error) {
	return nil, errors.New("boom")
}

type panicSource struct{}

func (panicSource) Name() string { return "panicky" }
func (panicSource) Resolve(context.Context, Components) ([]Candidate, error) {
	panic("source bug")
}

func TestPanickingSourceDoesNotCrash(t *testing.T) {
	p := newTestPipeline(FixtureSource{}, panicSource{})
	ans := p.Geocode(context.Background(), "allen avenue, ikeja", Hints{}, 3)
	if ans.Fix == nil || !contains(ans.Warnings, "source_unavailable") {
		t.Errorf("want an answer plus a warning, got fix=%v warnings=%v", ans.Fix, ans.Warnings)
	}
	if len(ans.SourcesFailed) != 1 || ans.SourcesFailed[0] != "panicky" {
		t.Errorf("failed sources: %v", ans.SourcesFailed)
	}
}

func newTestPipeline(sources ...Source) *Pipeline {
	return NewPipeline(NewNormalizer(DefaultTerms()), sources...)
}

func TestGeocodeResolvesToFinestClaimableClass(t *testing.T) {
	p := newTestPipeline(FixtureSource{})
	ans := p.Geocode(context.Background(), "opp. first bank, allen avenue, ikeja, lagos", Hints{}, 3)
	if ans.Fix == nil || ans.Fix.Precision != Street {
		t.Fatalf("want a street fix, got %+v", ans.Fix)
	}
	if got := ans.Fix.Precision.RadiusM(); got == nil || *got != 300 {
		t.Errorf("street radius should be 300 m, got %v", got)
	}
	if len(ans.Candidates) != 3 {
		t.Errorf("max_candidates should cap the list at 3, got %d", len(ans.Candidates))
	}
	if ans.Fix.Confidence != 0.7 {
		t.Errorf("an unmatched landmark should cap the street at 0.70, got %v", ans.Fix.Confidence)
	}
	if !contains(ans.Warnings, "landmark_not_matched") {
		t.Errorf("first bank is below the street threshold, so it must be named: %v", ans.Warnings)
	}
}

func TestGeocodeStepsUpToACoarserClass(t *testing.T) {
	p := newTestPipeline(FixtureSource{})
	ans := p.Geocode(context.Background(), "Garki, Abuja", Hints{}, 5)
	if ans.Fix == nil || ans.Fix.Precision != Locality {
		t.Fatalf("want a locality fix, got %+v", ans.Fix)
	}
}

func TestGeocodeNeverFakesAPin(t *testing.T) {
	p := newTestPipeline(FixtureSource{})
	ans := p.Geocode(context.Background(), "behind the mango tree", Hints{}, 3)
	if ans.Fix != nil {
		t.Fatalf("nothing resolves, so there must be no fix: %+v", ans.Fix)
	}
	if !contains(ans.Warnings, "landmark_not_matched") {
		t.Errorf("want landmark_not_matched, got %v", ans.Warnings)
	}
	if ans.Candidates == nil {
		t.Errorf("candidates must be an empty list, not nil")
	}
}

func TestLowConfidenceCandidateIsNotClaimed(t *testing.T) {
	p := newTestPipeline(FixtureSource{})
	p.Thresholds[Street] = 0.95 // stricter than the fixture's 0.80
	p.Thresholds[Locality] = 0.95
	p.Thresholds[State] = 0.95
	ans := p.Geocode(context.Background(), "allen avenue, ikeja, lagos", Hints{}, 5)
	if ans.Fix != nil {
		t.Fatalf("no candidate clears 0.95, got %+v", ans.Fix)
	}
	if !contains(ans.Warnings, "low_confidence") {
		t.Errorf("want low_confidence, got %v", ans.Warnings)
	}
}

func TestSlowAndBrokenSourcesDegradeGracefully(t *testing.T) {
	p := newTestPipeline(FixtureSource{}, slowSource{}, brokenSource{})
	p.SourceTimeout = 100 * time.Millisecond
	start := time.Now()
	ans := p.Geocode(context.Background(), "allen avenue, ikeja", Hints{}, 3)
	if time.Since(start) > time.Second {
		t.Errorf("a slow source must not hold the answer past its timeout")
	}
	if ans.Fix == nil {
		t.Errorf("the healthy source should still answer")
	}
	if !contains(ans.Warnings, "source_unavailable") {
		t.Errorf("want source_unavailable, got %v", ans.Warnings)
	}
	if len(ans.SourcesUsed) != 1 || ans.SourcesUsed[0] != "fixture" {
		t.Errorf("only the fixture should be reported as used: %v", ans.SourcesUsed)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
