package resolver

import (
	"context"
	"math"
	"testing"
)

type staticSource []Candidate

func (s staticSource) Name() string { return "static" }
func (s staticSource) Resolve(context.Context, Components) ([]Candidate, error) {
	return []Candidate(s), nil
}

func TestUnmatchedPartsLowerTheClaim(t *testing.T) {
	cases := []struct {
		name        string
		address     string
		cands       []Candidate
		wantFix     bool
		wantPrec    Precision
		wantConf    float64
		wantWarning []string
		wantMissing []string
		wantAbsent  []string
	}{
		{
			name:    "street missing keeps the locality at half confidence",
			address: "allen avenue, ikeja",
			cands: []Candidate{
				{Precision: Locality, Confidence: 0.9, Matched: []string{"area:ikeja"}},
				{Precision: State, Confidence: 0.9, Matched: []string{"state:lagos"}},
			},
			wantFix:     true,
			wantPrec:    Locality,
			wantConf:    0.5,
			wantWarning: []string{"street_not_matched"},
			wantMissing: []string{"street:allen avenue"},
		},
		{
			name:    "street missing blocks a state answer",
			address: "ikorodu road, lagos",
			cands: []Candidate{
				{Precision: State, Confidence: 0.9, Matched: []string{"state:lagos"}},
			},
			wantWarning: []string{"street_not_matched"},
			wantMissing: []string{"street:ikorodu road"},
		},
		{
			name:    "a fallback area can answer when the primary area is missing",
			address: "gra, port harcourt",
			cands: []Candidate{
				{Precision: Locality, Confidence: 0.9, Matched: []string{"area:port harcourt"}},
			},
			wantFix:     true,
			wantPrec:    Locality,
			wantConf:    0.5,
			wantWarning: []string{"area_not_matched"},
			wantMissing: []string{"area:gra"},
			wantAbsent:  []string{"area:port harcourt"},
		},
		{
			name:    "an unmatched area blocks a state answer",
			address: "wuse 2, abuja",
			cands: []Candidate{
				{Precision: State, Confidence: 0.9, Matched: []string{"state:federal capital territory"}},
			},
			wantWarning: []string{"area_not_matched"},
			wantMissing: []string{"area:wuse 2"},
		},
		{
			name:    "an unmatched LGA blocks a state answer",
			address: "bad lga, lagos",
			cands: []Candidate{
				{Precision: State, Confidence: 0.9, Matched: []string{"state:lagos"}},
			},
			wantWarning: []string{"lga_not_matched"},
			wantMissing: []string{"lga:bad"},
		},
		{
			name:    "an unmatched landmark caps the street",
			address: "opp. first bank, allen avenue, ikeja",
			cands: []Candidate{
				{Precision: Street, Confidence: 0.8, Matched: []string{"street:allen avenue"}},
				{Precision: Locality, Confidence: 0.9, Matched: []string{"area:ikeja"}},
				{Precision: Building, Confidence: 0.6, Matched: []string{"landmark:first bank"}},
			},
			wantFix:     true,
			wantPrec:    Street,
			wantConf:    0.7,
			wantWarning: []string{"landmark_not_matched"},
			wantMissing: []string{"landmark:first bank"},
		},
		{
			name:    "an unmatched house number caps the street",
			address: "12 allen avenue, ikeja",
			cands: []Candidate{
				{Precision: Street, Confidence: 0.8, Matched: []string{"street:allen avenue"}},
				{Precision: Locality, Confidence: 0.9, Matched: []string{"area:ikeja"}},
			},
			wantFix:     true,
			wantPrec:    Street,
			wantConf:    0.8,
			wantWarning: []string{"house_number_not_matched"},
			wantMissing: []string{"house_number:12"},
		},
		{
			name:    "a matched house number keeps the building",
			address: "12 allen avenue, ikeja",
			cands: []Candidate{
				{Precision: Building, Confidence: 0.95, Matched: []string{"house_number:12"}},
				{Precision: Street, Confidence: 0.8, Matched: []string{"street:allen avenue"}},
				{Precision: Locality, Confidence: 0.9, Matched: []string{"area:ikeja"}},
			},
			wantFix:  true,
			wantPrec: Building,
			wantConf: 0.95,
		},
		{
			name:    "an unmatched house number rejects a building from somewhere else",
			address: "12 allen avenue, ikeja",
			cands: []Candidate{
				{Precision: Building, Confidence: 0.95, Matched: []string{"landmark:first bank"}},
				{Precision: Street, Confidence: 0.8, Matched: []string{"street:allen avenue"}},
				{Precision: Locality, Confidence: 0.9, Matched: []string{"area:ikeja"}},
			},
			wantFix:     true,
			wantPrec:    Street,
			wantConf:    0.8,
			wantWarning: []string{"house_number_not_matched"},
			wantMissing: []string{"house_number:12"},
		},
		{
			name:    "every supplied part matched",
			address: "12 allen avenue, ikeja",
			cands: []Candidate{
				{Precision: Building, Confidence: 0.95, Matched: []string{"house_number:12"}},
				{Precision: Street, Confidence: 0.8, Matched: []string{"street:allen avenue"}},
				{Precision: Locality, Confidence: 0.9, Matched: []string{"area:ikeja"}},
			},
			wantFix:  true,
			wantPrec: Building,
			wantConf: 0.95,
		},
		{
			name:    "several missing parts are all named",
			address: "12 ikorodu road, gra, lagos",
			cands: []Candidate{
				{Precision: State, Confidence: 0.9, Matched: []string{"state:lagos"}},
				{Precision: Locality, Confidence: 0.9, Matched: []string{"area:port harcourt"}},
			},
			wantWarning: []string{"street_not_matched", "area_not_matched", "house_number_not_matched"},
			wantMissing: []string{"street:ikorodu road", "area:gra", "house_number:12"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(staticSource(tc.cands))
			ans := p.Geocode(context.Background(), tc.address, Hints{}, 5)

			if tc.wantFix {
				if ans.Fix == nil {
					t.Fatalf("want a fix, got none; warnings=%v unmatched=%v", ans.Warnings, ans.Unmatched)
				}
				if ans.Fix.Precision != tc.wantPrec {
					t.Errorf("precision: want %s, got %s", tc.wantPrec, ans.Fix.Precision)
				}
				if math.Abs(ans.Fix.Confidence-tc.wantConf) > 0.001 {
					t.Errorf("confidence: want %.2f, got %.2f", tc.wantConf, ans.Fix.Confidence)
				}
			} else if ans.Fix != nil {
				t.Fatalf("want no fix, got %+v", ans.Fix)
			}

			for _, w := range tc.wantWarning {
				if !contains(ans.Warnings, w) {
					t.Errorf("missing warning %q in %v", w, ans.Warnings)
				}
			}
			for _, m := range tc.wantMissing {
				if !contains(ans.Unmatched, m) {
					t.Errorf("missing unmatched part %q in %v", m, ans.Unmatched)
				}
			}
			for _, a := range tc.wantAbsent {
				if contains(ans.Unmatched, a) {
					t.Errorf("unexpected unmatched part %q in %v", a, ans.Unmatched)
				}
			}
		})
	}
}
