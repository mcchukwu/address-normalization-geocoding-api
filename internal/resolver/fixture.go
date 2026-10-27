package resolver

import "context"

// FixtureSource is a tiny in-memory gazetteer for development and tests. Its coordinates are
// approximate and exist only so the API returns answers shaped like real ones; it must never
// be enabled in production, where the PostGIS gazetteer replaces it.
type FixtureSource struct{}

type fixtureEntry struct {
	lat, lng float64
	prec     Precision
	conf     float64
}

var fixtureData = map[string]fixtureEntry{
	"state:lagos":                     {6.5244, 3.3792, State, 0.90},
	"state:federal capital territory": {9.0765, 7.3986, State, 0.90},
	"state:rivers":                    {4.8156, 7.0498, State, 0.90},
	"area:ikeja":                      {6.6018, 3.3515, Locality, 0.65},
	"area:victoria island":            {6.4281, 3.4219, Locality, 0.65},
	"area:garki":                      {9.0300, 7.4900, Locality, 0.60},
	"area:surulere":                   {6.5000, 3.3600, Locality, 0.65},
	"street:allen avenue":             {6.6018, 3.3515, Street, 0.80},
	"street:adeola odeku street":      {6.4300, 3.4140, Street, 0.80},
	"landmark:first bank":             {6.6020, 3.3520, Street, 0.55},
}

// Name implements Source.
func (FixtureSource) Name() string { return "fixture" }

// Resolve implements Source. Each matched part yields a candidate at that part's precision.
func (FixtureSource) Resolve(_ context.Context, c Components) ([]Candidate, error) {
	var out []Candidate
	add := func(key string) {
		if e, ok := fixtureData[key]; ok {
			out = append(out, Candidate{Lat: e.lat, Lng: e.lng, Precision: e.prec, Confidence: e.conf,
				Source: "fixture", Matched: []string{key}})
		}
	}
	if c.Street != "" {
		add("street:" + c.Street)
	}
	if c.Area != "" {
		add("area:" + c.Area)
	}
	if c.State != "" {
		add("state:" + c.State)
	}
	for _, l := range c.Landmarks {
		add("landmark:" + l.Name)
	}
	return out, nil
}
