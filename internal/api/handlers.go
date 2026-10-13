package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/store"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const (
	maxBatchItems     = 100
	defaultCandidates = 3
	rawTextRetention  = 30 * 24 * time.Hour
)

// decode reads one JSON object into dst and writes the right problem on failure:
// 400 for broken JSON, 413 for a body over the limit, 422 for nulls, unknown fields and wrong types.
func decode(w http.ResponseWriter, r *http.Request, st *reqState, dst any) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, st, newProblem(http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large", "The request body exceeds the size limit."))
		} else {
			fail(w, st, newProblem(http.StatusBadRequest, "invalid_json", "Invalid JSON", "The body could not be read."))
		}
		return false
	}

	// Pass 1: is it one well-formed JSON value, and does it contain a null? No field in any
	// request schema is nullable, so a null anywhere is a validation error.
	var generic any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		fail(w, st, newProblem(http.StatusBadRequest, "invalid_json", "Invalid JSON", "The body is not valid JSON."))
		return false
	}
	if _, extra := dec.Token(); extra != io.EOF {
		fail(w, st, newProblem(http.StatusBadRequest, "invalid_json", "Invalid JSON", "Unexpected data after the JSON value."))
		return false
	}
	if paths := findNulls(generic, ""); len(paths) > 0 {
		errs := make([]FieldError, len(paths))
		for i, p := range paths {
			errs[i] = FieldError{Field: p, Message: "must not be null"}
		}
		validationFailed(w, st, errs)
		return false
	}

	// Pass 2: into the typed struct, refusing unknown fields.
	dec2 := json.NewDecoder(bytes.NewReader(body))
	dec2.DisallowUnknownFields()
	if err := dec2.Decode(dst); err != nil {
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &typeErr):
			p := newProblem(http.StatusUnprocessableEntity, "validation_failed", "Validation failed", "A field has the wrong type.")
			p.Errors = []FieldError{{Field: typeErr.Field, Message: "expected " + typeErr.Type.String()}}
			fail(w, st, p)
		case strings.HasPrefix(err.Error(), "json: unknown field"):
			p := newProblem(http.StatusUnprocessableEntity, "validation_failed", "Validation failed", "The request has a field this endpoint does not accept.")
			p.Errors = []FieldError{{Field: strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field"), ` "`), Message: "unknown field"}}
			fail(w, st, p)
		default:
			fail(w, st, newProblem(http.StatusBadRequest, "invalid_json", "Invalid JSON", "The body could not be read."))
		}
		return false
	}
	stripNUL(reflect.ValueOf(dst))
	return true
}

// stripNUL removes NUL characters from every string in a decoded request. JSON allows them but
// PostgreSQL text and jsonb cannot store them, so left in they would turn a valid request into a 500.
func stripNUL(v reflect.Value) {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			stripNUL(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				stripNUL(v.Field(i))
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			stripNUL(v.Index(i))
		}
	case reflect.String:
		if v.CanSet() && strings.ContainsRune(v.String(), 0) {
			v.SetString(strings.ReplaceAll(v.String(), "\x00", ""))
		}
	}
}

// findNulls lists the path of every JSON null in a decoded value.
func findNulls(v any, path string) []string {
	switch t := v.(type) {
	case nil:
		if path == "" {
			return []string{"(body)"}
		}
		return []string{path}
	case map[string]any:
		var out []string
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			out = append(out, findNulls(t[k], p)...)
		}
		return out
	case []any:
		var out []string
		for i, e := range t {
			out = append(out, findNulls(e, fmt.Sprintf("%s[%d]", path, i))...)
		}
		return out
	}
	return nil
}

func validationFailed(w http.ResponseWriter, st *reqState, errs []FieldError) {
	p := newProblem(http.StatusUnprocessableEntity, "validation_failed", "Validation failed", "One or more fields are invalid.")
	p.Errors = errs
	fail(w, st, p)
}

func checkAddress(field, a string) []FieldError {
	n := utf8.RuneCountInString(a)
	switch {
	case n < 3 || strings.TrimSpace(a) == "":
		return []FieldError{{Field: field, Message: "must be at least 3 characters and not blank"}}
	case n > 500:
		return []FieldError{{Field: field, Message: "must be at most 500 characters"}}
	}
	return nil
}

func checkHints(prefix string, h *hintsDTO) []FieldError {
	var errs []FieldError
	if h != nil {
		if utf8.RuneCountInString(h.State) > 60 {
			errs = append(errs, FieldError{Field: prefix + "hints.state", Message: "must be at most 60 characters"})
		}
		if utf8.RuneCountInString(h.LGA) > 100 {
			errs = append(errs, FieldError{Field: prefix + "hints.lga", Message: "must be at most 100 characters"})
		}
	}
	return errs
}

func checkMax(m *wholeNumber) (int, []FieldError) {
	if m == nil {
		return defaultCandidates, nil
	}
	if *m < 1 || *m > 10 {
		return 0, []FieldError{{Field: "max_candidates", Message: "must be between 1 and 10"}}
	}
	return int(*m), nil
}

// ---- POST /v1/normalize

func (s *Server) handleNormalize(w http.ResponseWriter, r *http.Request, st *reqState) {
	var req normalizeRequest
	if !decode(w, r, st, &req) {
		return
	}
	errs := append(checkAddress("address", req.Address), checkHints("", req.Hints)...)
	if len(errs) > 0 {
		validationFailed(w, st, errs)
		return
	}
	comps, warnings := s.o.Pipeline.Normalize(req.Address, req.Hints.toHints())
	st.StateID = s.stateID(comps)
	st.LGA = comps.LGA
	st.InputHash = s.inputHash(comps)
	st.Warnings = warnings
	writeJSON(w, http.StatusOK, normalizeResponse{Input: req.Address, Normalized: toNormalizedDTO(comps),
		Warnings: nonNil(warnings), RequestID: st.ID})
}

// ---- POST /v1/geocode

func (s *Server) handleGeocode(w http.ResponseWriter, r *http.Request, st *reqState) {
	var req geocodeRequest
	if !decode(w, r, st, &req) {
		return
	}
	max, merrs := checkMax(req.MaxCandidates)
	errs := append(append(checkAddress("address", req.Address), checkHints("", req.Hints)...), merrs...)
	if len(errs) > 0 {
		validationFailed(w, st, errs)
		return
	}
	ans := s.o.Pipeline.Geocode(r.Context(), req.Address, req.Hints.toHints(), max)
	s.recordAnswer(st, ans)
	if st.Tenant.RetainRaw {
		st.Raw = &store.RawText{RequestID: st.ID, ReceivedAt: st.Start.UTC(), Text: req.Address,
			ExpiresAt: st.Start.UTC().Add(rawTextRetention)}
	}
	writeJSON(w, http.StatusOK, geocodeResponse{answerDTO: toAnswerDTO(req.Address, ans),
		DataVersion: s.o.DataVersion, RequestID: st.ID})
}

// ---- POST /v1/geocode:batch

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request, st *reqState) {
	var req batchRequest
	if !decode(w, r, st, &req) {
		return
	}
	max, merrs := checkMax(req.MaxCandidates)
	var errs []FieldError
	errs = append(errs, merrs...)
	if n := len(req.Items); n < 1 || n > maxBatchItems {
		errs = append(errs, FieldError{Field: "items", Message: "must contain between 1 and 100 items"})
	}
	for i, it := range req.Items {
		prefix := fmt.Sprintf("items[%d].", i)
		errs = append(errs, checkAddress(prefix+"address", it.Address)...)
		errs = append(errs, checkHints(prefix, it.Hints)...)
		if utf8.RuneCountInString(it.ClientRef) > 64 {
			errs = append(errs, FieldError{Field: prefix + "client_ref", Message: "must be at most 64 characters"})
		}
	}
	if len(errs) > 0 {
		validationFailed(w, st, errs)
		return
	}
	// The call already cost one request; every further 10 addresses cost one more.
	if extra := len(req.Items) / 10; extra > 0 && !s.charge(w, st, extra) {
		return
	}

	results := make([]batchResult, len(req.Items))
	answers := make([]resolver.Answer, len(req.Items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, it := range req.Items {
		results[i] = batchResult{Index: i, ClientRef: it.ClientRef}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, it batchItem) {
			defer wg.Done()
			defer func() { <-sem }()
			answers[i] = s.o.Pipeline.Geocode(r.Context(), it.Address, it.Hints.toHints(), max)
			d := toAnswerDTO(it.Address, answers[i])
			results[i].Status, results[i].Answer = "ok", &d
		}(i, it)
	}
	wg.Wait()

	class := st.Tenant.TrafficClass
	if class == "" {
		class = "real"
	}
	for i := range results {
		if results[i].Status == "ok" {
			prec := string(resolver.None)
			if answers[i].Fix != nil {
				prec = string(answers[i].Fix.Precision)
			}
			s.o.Metrics.ObserveResult(prec, class)
		}
	}
	writeJSON(w, http.StatusOK, batchResponse{Results: results, DataVersion: s.o.DataVersion, RequestID: st.ID})
}

// ---- POST /v1/corrections

func (s *Server) handleCorrection(w http.ResponseWriter, r *http.Request, st *reqState) {
	var req correctionRequest
	if !decode(w, r, st, &req) {
		return
	}
	var errs []FieldError
	if !uuidRE.MatchString(strings.ToLower(req.RequestID)) {
		errs = append(errs, FieldError{Field: "request_id", Message: "must be a UUID"})
	}
	errs = append(errs, checkAddress("address", req.Address)...)
	if req.Confirmed == nil || req.Confirmed.Lat == nil || req.Confirmed.Lng == nil {
		errs = append(errs, FieldError{Field: "confirmed", Message: "lat and lng are required"})
	} else {
		if lat := *req.Confirmed.Lat; lat < 4.0 || lat > 14.5 {
			errs = append(errs, FieldError{Field: "confirmed.lat", Message: "must be between 4.0 and 14.5 (Nigeria)"})
		}
		if lng := *req.Confirmed.Lng; lng < 2.0 || lng > 15.0 {
			errs = append(errs, FieldError{Field: "confirmed.lng", Message: "must be between 2.0 and 15.0 (Nigeria)"})
		}
	}
	if utf8.RuneCountInString(req.Note) > 500 {
		errs = append(errs, FieldError{Field: "note", Message: "must be at most 500 characters"})
	}
	if len(errs) > 0 {
		validationFailed(w, st, errs)
		return
	}

	reqID := strings.ToLower(req.RequestID)
	info, found, err := s.o.Store.FindRequest(r.Context(), st.Principal.TenantID, reqID)
	if err != nil {
		s.o.Logger.Error("correction lookup failed", "err", err.Error())
		fail(w, st, newProblem(http.StatusInternalServerError, "internal_error", "Internal error", "Something failed on our side."))
		return
	}
	if !found { // also the answer for another tenant's request, so existence is not leaked
		fail(w, st, newProblem(http.StatusNotFound, "request_not_found", "Not found", "No recent request with that id exists for your tenant."))
		return
	}

	comps, _ := s.o.Pipeline.Normalize(req.Address, resolver.Hints{})
	normJSON, _ := json.Marshal(toNormalizedDTO(comps))
	orig := info.Precision
	if orig == "" {
		orig = string(resolver.None)
	}
	c := store.Correction{ID: newUUID(), TenantID: st.Principal.TenantID, RequestID: reqID,
		InputHash: s.inputHash(comps), NormalizedJSON: normJSON, OriginalPrecision: orig,
		OriginalConfidence: info.Confidence, Lat: *req.Confirmed.Lat, Lng: *req.Confirmed.Lng, Note: req.Note}
	if st.Tenant.RetainRaw { // raw text is kept only for tenants that opted in
		exp := s.o.Now().UTC().Add(rawTextRetention)
		c.InputText, c.InputTextExpiresAt = req.Address, &exp
	}
	created, err := s.o.Store.InsertCorrection(r.Context(), c)
	switch {
	case errors.Is(err, store.ErrDuplicate):
		fail(w, st, newProblem(http.StatusConflict, "duplicate_correction", "Conflict", "A correction for this request already exists."))
		return
	case err != nil:
		s.o.Logger.Error("correction insert failed", "err", err.Error())
		fail(w, st, newProblem(http.StatusInternalServerError, "internal_error", "Internal error", "Something failed on our side."))
		return
	}
	writeJSON(w, http.StatusCreated, correctionResponse{ID: c.ID, Status: "pending", CreatedAt: created.UTC().Format(time.RFC3339)})
}

// ---- probes and unknown routes

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request, _ *reqState) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request, st *reqState) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.o.Store.Ping(ctx); err != nil {
		fail(w, st, newProblem(http.StatusServiceUnavailable, "not_ready", "Not ready", "The database is not reachable."))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleUnknown answers 404, or 405 with an Allow header when the path exists for other methods.
func (s *Server) handleUnknown(w http.ResponseWriter, r *http.Request) {
	st := stateOf(r)
	var allowed []string
	for _, pat := range s.routes {
		method, path, _ := strings.Cut(pat, " ")
		if path == r.URL.Path {
			allowed = append(allowed, method)
		}
	}
	if len(allowed) > 0 {
		sort.Strings(allowed)
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		fail(w, st, newProblem(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "Use one of: "+strings.Join(allowed, ", ")+"."))
		return
	}
	fail(w, st, newProblem(http.StatusNotFound, "not_found", "Not found", "No such endpoint."))
}
