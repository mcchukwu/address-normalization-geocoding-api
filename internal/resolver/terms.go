package resolver

// StateInfo is one of the 36 states or the Federal Capital Territory.
type StateInfo struct {
	ID      int
	Name    string
	Norm    string
	Aliases []string
}

// Terms is the normalizer's dictionary. The defaults below mirror ref.address_terms and
// ref.states in the database; an integration test checks they stay in step.
type Terms struct {
	Abbreviations map[string]string // surface -> canonical word
	Relations     map[string]string // surface -> canonical relation
	FeatureWords  map[string]string // surface -> feature category
	States        []StateInfo
}

// DefaultTerms returns the embedded dictionary.
func DefaultTerms() Terms {
	return Terms{
		Abbreviations: map[string]string{
			"opp": "opposite", "b/stop": "bus stop", "bstop": "bus stop", "rd": "road", "ave": "avenue",
			"st": "street", "cres": "crescent", "cl": "close", "dr": "drive", "ln": "lane", "est": "estate",
			"jct": "junction", "nr": "near", "hse": "house", "bldg": "building", "plt": "plot", "blk": "block",
		},
		Relations: map[string]string{
			"opposite": "opposite", "beside": "beside", "besides": "beside", "next to": "beside",
			"adjacent to": "beside", "behind": "behind", "after": "after", "before": "before",
			"near": "near", "close to": "near", "inside": "inside", "within": "inside", "off": "off",
		},
		FeatureWords: map[string]string{
			"road": "road", "street": "road", "avenue": "road", "close": "road", "crescent": "road",
			"drive": "road", "lane": "road", "estate": "locality", "junction": "junction",
			"roundabout": "junction", "market": "market", "church": "worship", "mosque": "worship",
			"school": "education", "college": "education", "university": "education",
			"hospital": "health", "clinic": "health", "bank": "bank", "hotel": "lodging",
			"filling station": "fuel", "petrol station": "fuel", "bus stop": "transport",
			"plaza": "shopping", "mall": "shopping",
		},
		States: []StateInfo{
			{1, "Abia", "abia", nil}, {2, "Adamawa", "adamawa", nil},
			{3, "Akwa Ibom", "akwa ibom", []string{"akwa-ibom"}}, {4, "Anambra", "anambra", nil},
			{5, "Bauchi", "bauchi", nil}, {6, "Bayelsa", "bayelsa", nil}, {7, "Benue", "benue", nil},
			{8, "Borno", "borno", nil}, {9, "Cross River", "cross river", []string{"cross-river"}},
			{10, "Delta", "delta", nil}, {11, "Ebonyi", "ebonyi", nil}, {12, "Edo", "edo", nil},
			{13, "Ekiti", "ekiti", nil}, {14, "Enugu", "enugu", nil},
			{15, "Federal Capital Territory", "federal capital territory", []string{"fct", "abuja", "fct abuja"}},
			{16, "Gombe", "gombe", nil}, {17, "Imo", "imo", nil}, {18, "Jigawa", "jigawa", nil},
			{19, "Kaduna", "kaduna", nil}, {20, "Kano", "kano", nil}, {21, "Katsina", "katsina", nil},
			{22, "Kebbi", "kebbi", nil}, {23, "Kogi", "kogi", nil}, {24, "Kwara", "kwara", nil},
			{25, "Lagos", "lagos", nil}, {26, "Nasarawa", "nasarawa", []string{"nassarawa"}},
			{27, "Niger", "niger", nil}, {28, "Ogun", "ogun", nil}, {29, "Ondo", "ondo", nil},
			{30, "Osun", "osun", nil}, {31, "Oyo", "oyo", nil}, {32, "Plateau", "plateau", nil},
			{33, "Rivers", "rivers", nil}, {34, "Sokoto", "sokoto", nil}, {35, "Taraba", "taraba", nil},
			{36, "Yobe", "yobe", nil}, {37, "Zamfara", "zamfara", nil},
		},
	}
}
