package resolver

import (
	"regexp"
	"sort"
	"strings"
)

// Normalizer splits address text into parts using rules and a dictionary. It is
// deliberately rule-based: there is no labelled Nigerian training data, and rules can
// be read, tested and corrected one case at a time.
type Normalizer struct {
	t          Terms
	relations  []string // longest first, so "next to" is tried before "near"
	roadWord   map[string]bool
	stateNames map[string]StateInfo // normalised name or alias -> state
	stateKeys  []string             // longest first
	ambiguous  map[string]bool      // abbreviations that are only expanded after the first word
}

var (
	houseNumber = regexp.MustCompile(`^\d+[a-z]?(-\d+[a-z]?)?$`)
	sixDigits   = regexp.MustCompile(`^\d{6}$`)
	spaces      = regexp.MustCompile(`\s+`)
)

// NewNormalizer builds a Normalizer from a dictionary.
func NewNormalizer(t Terms) *Normalizer {
	n := &Normalizer{
		t:          t,
		roadWord:   map[string]bool{},
		stateNames: map[string]StateInfo{},
		// "st" is also Saint and "dr" also Doctor: only expand them after the first word.
		ambiguous: map[string]bool{"st": true, "dr": true},
	}
	for rel := range t.Relations {
		n.relations = append(n.relations, rel)
	}
	sort.Slice(n.relations, func(i, j int) bool { return len(n.relations[i]) > len(n.relations[j]) })
	for w, cat := range t.FeatureWords {
		if cat == "road" {
			n.roadWord[w] = true
		}
	}
	for _, s := range t.States {
		n.stateNames[s.Norm] = s
		for _, a := range s.Aliases {
			n.stateNames[a] = s
		}
	}
	for k := range n.stateNames {
		n.stateKeys = append(n.stateKeys, k)
	}
	sort.Slice(n.stateKeys, func(i, j int) bool { return len(n.stateKeys[i]) > len(n.stateKeys[j]) })
	return n
}

// StateByName finds a state from its name or alias, in any case.
func (n *Normalizer) StateByName(name string) (StateInfo, bool) {
	s, ok := n.stateNames[clean(name)]
	return s, ok
}

func clean(s string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(strings.ToLower(s), " "))
}

// Normalize returns the parts of raw and any warnings about the hints.
func (n *Normalizer) Normalize(raw string, h Hints) (Components, []string) {
	var c Components
	var warnings []string
	var areas []string

	for _, seg := range splitSegments(raw) {
		seg = n.expand(seg)

		if rel, rest, ok := n.leadingRelation(seg); ok {
			if rest != "" {
				c.Landmarks = append(c.Landmarks, Landmark{Name: rest, Relation: rel})
			}
			continue
		}

		seg, c.Postcode = takePostcode(seg, c.Postcode)
		seg, c.State = n.takeState(seg, c.State)
		seg, c.LGA = takeLGA(seg, c.LGA)
		if c.HouseNumber == "" {
			seg, c.HouseNumber = takeHouseNumber(seg)
		}
		if seg == "" {
			continue
		}

		if street, rest, ok := n.splitAtRoadWord(seg); ok {
			if c.Street == "" {
				c.Street = street
			} else {
				c.Landmarks = append(c.Landmarks, Landmark{Name: street})
			}
			if rest != "" {
				areas = append(areas, rest)
			}
			continue
		}
		if n.hasFeatureWord(seg) {
			c.Landmarks = append(c.Landmarks, Landmark{Name: seg})
			continue
		}
		areas = append(areas, seg)
	}

	// The first unclassified segment is the most specific place name ("ikeja gra", then "ikeja").
	if len(areas) > 0 {
		c.Area = areas[0]
	}

	if h.State != "" {
		if s, ok := n.StateByName(h.State); ok {
			if c.State == "" {
				c.State = s.Norm
			}
		} else {
			warnings = append(warnings, "unknown_state_hint")
		}
	}
	if c.LGA == "" && h.LGA != "" {
		c.LGA = clean(h.LGA)
	}
	return c, warnings
}

// Canonical is a stable text form of the parts, used for hashing, never for display.
func (c Components) Canonical() string {
	parts := []string{c.HouseNumber, c.Street, c.Area, c.LGA, c.State, c.Postcode}
	for _, l := range c.Landmarks {
		parts = append(parts, l.Relation+":"+l.Name)
	}
	return strings.Join(parts, "|")
}

func splitSegments(raw string) []string {
	raw = strings.NewReplacer(";", ",", "\n", ",", "\r", ",").Replace(raw)
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = clean(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// expand replaces abbreviations word by word ("opp." -> "opposite").
func (n *Normalizer) expand(seg string) string {
	words := strings.Fields(seg)
	for i, w := range words {
		key := strings.TrimRight(w, ".")
		if full, ok := n.t.Abbreviations[key]; ok {
			if n.ambiguous[key] && i == 0 {
				words[i] = key
				continue
			}
			words[i] = full
		} else {
			words[i] = key
		}
	}
	return strings.Join(words, " ")
}

func (n *Normalizer) leadingRelation(seg string) (relation, rest string, ok bool) {
	for _, r := range n.relations {
		if seg == r {
			return n.t.Relations[r], "", true
		}
		if strings.HasPrefix(seg, r+" ") {
			return n.t.Relations[r], strings.TrimSpace(seg[len(r):]), true
		}
	}
	return "", "", false
}

func takePostcode(seg, have string) (string, string) {
	words := strings.Fields(seg)
	kept := words[:0]
	for _, w := range words {
		if have == "" && sixDigits.MatchString(w) {
			have = w
			continue
		}
		kept = append(kept, w)
	}
	return strings.Join(kept, " "), have
}

// takeState removes a trailing (or whole-segment) state name and records it.
func (n *Normalizer) takeState(seg, have string) (string, string) {
	seg = strings.TrimSuffix(seg, " state")
	for _, k := range n.stateKeys {
		if seg == k || strings.HasSuffix(seg, " "+k) {
			if have == "" {
				have = n.stateNames[k].Norm
			}
			return strings.TrimSpace(strings.TrimSuffix(seg, k)), have
		}
	}
	return seg, have
}

func takeLGA(seg, have string) (string, string) {
	for _, suffix := range []string{" local government area", " local government", " lga"} {
		if strings.HasSuffix(seg, suffix) {
			name := strings.TrimSpace(strings.TrimSuffix(seg, suffix))
			if have == "" {
				have = name
			}
			return "", have
		}
	}
	return seg, have
}

func takeHouseNumber(seg string) (string, string) {
	words := strings.Fields(seg)
	if len(words) >= 2 && (words[0] == "no" || words[0] == "plot" || words[0] == "house") && houseNumber.MatchString(words[1]) {
		return strings.Join(words[2:], " "), words[1]
	}
	if len(words) >= 2 && houseNumber.MatchString(words[0]) {
		return strings.Join(words[1:], " "), words[0]
	}
	return seg, ""
}

// splitAtRoadWord finds the last road word ("street", "avenue") and treats everything up to
// it as the street name and anything after it as a place name.
func (n *Normalizer) splitAtRoadWord(seg string) (street, rest string, ok bool) {
	words := strings.Fields(seg)
	for i := len(words) - 1; i >= 0; i-- {
		if n.roadWord[words[i]] {
			return strings.Join(words[:i+1], " "), strings.Join(words[i+1:], " "), true
		}
	}
	return "", "", false
}

func (n *Normalizer) hasFeatureWord(seg string) bool {
	padded := " " + seg + " "
	for w := range n.t.FeatureWords {
		if strings.Contains(padded, " "+w+" ") {
			return true
		}
	}
	return false
}
