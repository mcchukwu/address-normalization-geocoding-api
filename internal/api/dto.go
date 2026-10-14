package api

import (
	"encoding/json"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
)

// These types are the wire format; openapi/openapi.yaml is their source of truth.

type hintsDTO struct {
	State string `json:"state"`
	LGA   string `json:"lga"`
}

func (h *hintsDTO) toHints() resolver.Hints {
	if h == nil {
		return resolver.Hints{}
	}
	return resolver.Hints{State: h.State, LGA: h.LGA}
}

type normalizeRequest struct {
	Address string    `json:"address"`
	Hints   *hintsDTO `json:"hints"`
}

// geocodeRequest uses json.RawMessage for max_candidates to accept 2.0 (valid per JSON Schema)
// while rejecting strings, 2.5, and overflow (1e400) with a proper field name in the error.
type geocodeRequest struct {
	Address       string          `json:"address"`
	Hints         *hintsDTO       `json:"hints"`
	MaxCandidates json.RawMessage `json:"max_candidates"`
}

type batchItem struct {
	Address   string       `json:"address"`
	Hints     *hintsDTO    `json:"hints"`
	ClientRef string       `json:"client_ref"`
}

type batchRequest struct {
	Items         []batchItem    `json:"items"`
	MaxCandidates json.RawMessage `json:"max_candidates"`
}

type correctionRequest struct {
	RequestID string `json:"request_id"`
	Address   string `json:"address"`
	Confirmed *struct {
		Lat *float64 `json:"lat"`
		Lng *float64 `json:"lng"`
	} `json:"confirmed"`
	Note string `json:"note"`
}

type landmarkDTO struct {
	Name     string `json:"name"`
	Relation string `json:"relation,omitempty"`
}

type normalizedDTO struct {
	HouseNumber string        `json:"house_number,omitempty"`
	Street      string        `json:"street,omitempty"`
	Area        string        `json:"area,omitempty"`
	LGA         string        `json:"lga,omitempty"`
	State       string        `json:"state,omitempty"`
	Postcode    string        `json:"postcode,omitempty"`
	Landmarks   []landmarkDTO `json:"landmarks,omitempty"`
}

func toNormalizedDTO(c resolver.Components) normalizedDTO {
	d := normalizedDTO{HouseNumber: c.HouseNumber, Street: c.Street, Area: c.Area, LGA: c.LGA, State: c.State, Postcode: c.Postcode}
	for _, l := range c.Landmarks {
		d.Landmarks = append(d.Landmarks, landmarkDTO{Name: l.Name, Relation: l.Relation})
	}
	return d
}

type fixDTO struct {
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	Precision  string  `json:"precision"`
	RadiusM    *int    `json:"radius_m"`
	Confidence float64 `json:"confidence"`
}

type candidateDTO struct {
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	Precision  string  `json:"precision"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
}

type answerDTO struct {
	Input      string         `json:"input"`
	Normalized normalizedDTO  `json:"normalized"`
	Result     *fixDTO        `json:"result"`
	Candidates []candidateDTO `json:"candidates"`
	Warnings   []string       `json:"warnings"`
}

func toAnswerDTO(input string, a resolver.Answer) answerDTO {
	d := answerDTO{Input: input, Normalized: toNormalizedDTO(a.Normalized),
		Candidates: []candidateDTO{}, Warnings: nonNil(a.Warnings)}
	if a.Fix != nil {
		d.Result = &fixDTO{Lat: a.Fix.Lat, Lng: a.Fix.Lng, Precision: string(a.Fix.Precision),
			RadiusM: a.Fix.Precision.RadiusM(), Confidence: a.Fix.Confidence}
	}
	for _, c := range a.Candidates {
		d.Candidates = append(d.Candidates, candidateDTO{Lat: c.Lat, Lng: c.Lng, Precision: string(c.Precision),
			Confidence: c.Confidence, Source: c.Source})
	}
	return d
}

type geocodeResponse struct {
	answerDTO
	DataVersion string `json:"data_version"`
	RequestID   string `json:"request_id"`
}

type normalizeResponse struct {
	Input      string        `json:"input"`
	Normalized normalizedDTO `json:"normalized"`
	Warnings   []string      `json:"warnings"`
	RequestID  string        `json:"request_id"`
}

type batchResult struct {
	Index     int        `json:"index"`
	ClientRef string     `json:"client_ref,omitempty"`
	Status    string     `json:"status"`
	Answer    *answerDTO `json:"answer,omitempty"`
	Error     *Problem   `json:"error,omitempty"`
}

type batchResponse struct {
	Results     []batchResult `json:"results"`
	DataVersion string        `json:"data_version"`
	RequestID   string        `json:"request_id"`
}

type correctionResponse struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
