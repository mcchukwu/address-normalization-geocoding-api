package resolver

import (
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	n := NewNormalizer(DefaultTerms())
	cases := []struct {
		name  string
		in    string
		hints Hints
		want  Components
		warn  []string
	}{
		{
			name: "landmark first, then street, area and state",
			in:   "opp. first bank, allen avenue, ikeja, lagos",
			want: Components{Street: "allen avenue", Area: "ikeja", State: "lagos",
				Landmarks: []Landmark{{Name: "first bank", Relation: "opposite"}}},
		},
		{
			name: "house number, abbreviation and a state with no comma",
			in:   "12 Adeola Odeku St, Victoria Island Lagos",
			want: Components{HouseNumber: "12", Street: "adeola odeku street", Area: "victoria island", State: "lagos"},
		},
		{
			name: "bus stop abbreviation and next to",
			in:   "Plot 5, Ogunlana Drive, next to Shoprite plaza, Surulere",
			want: Components{HouseNumber: "5", Street: "ogunlana drive", Area: "surulere",
				Landmarks: []Landmark{{Name: "shoprite plaza", Relation: "beside"}}},
		},
		{
			name: "b/stop is expanded and kept as a landmark",
			in:   "behind ikeja b/stop, ikeja",
			want: Components{Area: "ikeja", Landmarks: []Landmark{{Name: "ikeja bus stop", Relation: "behind"}}},
		},
		{
			name: "no street or house number",
			in:   "Garki, Abuja",
			want: Components{Area: "garki", State: "federal capital territory"},
		},
		{
			name: "six digit postcode and LGA",
			in:   "3 Zaria Rd, Kano 700001, Kano Municipal LGA",
			want: Components{HouseNumber: "3", Street: "zaria road", State: "kano", Postcode: "700001", LGA: "kano municipal"},
		},
		{
			name: "saint is not expanded to street",
			in:   "St. Mary's Church, Yaba",
			want: Components{Area: "yaba", Landmarks: []Landmark{{Name: "st mary's church"}}},
		},
		{
			name:  "hint fills a missing state",
			in:    "allen avenue, ikeja",
			hints: Hints{State: "Lagos"},
			want:  Components{Street: "allen avenue", Area: "ikeja", State: "lagos"},
		},
		{
			name:  "unknown state hint is reported",
			in:    "allen avenue, ikeja",
			hints: Hints{State: "Atlantis"},
			want:  Components{Street: "allen avenue", Area: "ikeja"},
			warn:  []string{"unknown_state_hint"},
		},
		{
			name: "text with no recognisable structure",
			in:   "behind the mango tree",
			want: Components{Landmarks: []Landmark{{Name: "the mango tree", Relation: "behind"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warn := n.Normalize(tc.in, tc.hints)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("components\n got  %+v\n want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(warn, tc.warn) {
				t.Errorf("warnings: got %v want %v", warn, tc.warn)
			}
		})
	}
}

func TestCanonicalIsStable(t *testing.T) {
	n := NewNormalizer(DefaultTerms())
	a, _ := n.Normalize("Opp. First Bank, Allen Ave, Ikeja", Hints{})
	b, _ := n.Normalize("opposite first bank,  allen avenue , ikeja", Hints{})
	if a.Canonical() != b.Canonical() {
		t.Errorf("different spellings should hash alike:\n %q\n %q", a.Canonical(), b.Canonical())
	}
}

func TestNameKey(t *testing.T) {
	n := NewNormalizer(DefaultTerms())
	same := [][]string{
		{"Allen Ave", "allen avenue", "ALLEN AVENUE.", "Allen-Avenue"},
		{"Eti-Osa", "eti osa"},
		{"Adeola Odeku St", "adeola odeku street"},
		{"St. Mary's Church", "st marys church"},
		{"V.I.", "v i"},
	}
	for _, group := range same {
		want := n.NameKey(group[0])
		for _, g := range group {
			if got := n.NameKey(g); got != want {
				t.Errorf("NameKey(%q) = %q, want %q", g, got, want)
			}
		}
	}
	if got := n.NameKey("Allen Ave"); got != "allen avenue" {
		t.Errorf("got %q", got)
	}
	for _, s := range []string{"Allen Ave", "St. Mary's Church", "Eti-Osa", "B/Stop Ikeja"} {
		if n.NameKey(n.NameKey(s)) != n.NameKey(s) {
			t.Errorf("NameKey must be idempotent for %q", s)
		}
	}
}
