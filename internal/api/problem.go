package api

import (
	"encoding/json"
	"net/http"
)

// FieldError names one invalid field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Problem is an RFC 9457 problem document with a stable machine-readable code.
type Problem struct {
	Type       string       `json:"type"`
	Title      string       `json:"title"`
	Status     int          `json:"status"`
	Detail     string       `json:"detail,omitempty"`
	Code       string       `json:"code"`
	RequestID  string       `json:"request_id,omitempty"`
	Errors     []FieldError `json:"errors,omitempty"`
	RetryAfter int          `json:"retry_after_seconds,omitempty"`
}

func newProblem(status int, code, title, detail string) Problem {
	return Problem{Type: "/problems/" + code, Title: title, Status: status, Detail: detail, Code: code}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail writes a problem document and records its code for the log and metrics.
func fail(w http.ResponseWriter, st *reqState, p Problem) {
	p.RequestID = st.ID
	st.ErrorCode = p.Code
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
