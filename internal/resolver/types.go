// Package resolver turns address text into structured parts and a coordinate with an
// honest precision. It has no HTTP or database knowledge: sources plug in behind an interface.
package resolver

// Precision is the claim an answer makes about how close the pin is to the truth.
type Precision string

const (
	Building Precision = "building"
	Street   Precision = "street"
	Locality Precision = "locality"
	LGA      Precision = "lga"
	State    Precision = "state"
	None     Precision = "none"
)

var rank = map[Precision]int{Building: 1, Street: 2, Locality: 3, LGA: 4, State: 5, None: 6}

// Rank is 1 for the finest class and larger for coarser ones.
func (p Precision) Rank() int { return rank[p] }

// RadiusM is the claimed error radius in metres; nil for lga and state (the area's own extent).
// The numbers mirror ref.precision_classes in the database.
func (p Precision) RadiusM() *int {
	var r int
	switch p {
	case Building:
		r = 50
	case Street:
		r = 300
	case Locality:
		r = 2000
	default:
		return nil
	}
	return &r
}

// Landmark is a named place the address refers to, with how the address relates to it.
type Landmark struct {
	Name     string
	Relation string // opposite, beside, behind, after, before, near, inside, off; empty if just named
}

// Components is the normalized form of an address. Empty fields were not found.
type Components struct {
	HouseNumber string
	Street      string
	Area        string
	LGA         string
	State       string
	Postcode    string
	Other       []string // further place names that fit no category (a misspelt road word, a city after a district)
	Landmarks   []Landmark
}

// Hints narrow the search when the caller already knows the state or LGA.
type Hints struct {
	State string
	LGA   string
}

// Candidate is one possible location offered by a source.
type Candidate struct {
	Lat, Lng   float64
	Precision  Precision
	Confidence float64
	Source     string
	Matched    []string // the matched part and its input text, e.g. "street:allen avenue", "landmark:first bank"
	Ambiguous  bool     // several plausible places share this name
}

// Fix is the chosen answer.
type Fix struct {
	Lat, Lng   float64
	Precision  Precision
	Confidence float64
}

// Answer is everything the pipeline learned about one address.
type Answer struct {
	Normalized    Components
	Fix           *Fix // nil when nothing resolves
	Candidates    []Candidate
	Unmatched     []string // supplied parts that did not match at their threshold, e.g. "street:ikorodu road"
	Warnings      []string
	SourcesUsed   []string
	SourcesFailed []string
}
