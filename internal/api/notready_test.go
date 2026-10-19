package api

import "testing"

func TestGeocodingAnswers503UntilTheGazetteerIsBuilt(t *testing.T) {
	h := newHarness(t)
	ready := false
	h.srv.o.RequireGazetteer = true
	h.srv.o.DataVersionFn = func() (string, bool) {
		if !ready {
			return "", false
		}
		return "2026-10-gaz-1", true
	}
	expectProblem(t, h.do("POST", "/v1/geocode", "keyA", landmarkAddr), 503, "not_ready")
	expectProblem(t, h.do("POST", "/v1/geocode:batch", "keyA", `{"items":[{"address":"allen avenue"}]}`), 503, "not_ready")
	expectProblem(t, h.do("GET", "/readyz", "", ""), 503, "not_ready")
	if rec := h.do("POST", "/v1/normalize", "keyA", landmarkAddr); rec.Code != 200 {
		t.Errorf("normalize needs no gazetteer: %d", rec.Code)
	}
	if rec := h.do("GET", "/healthz", "", ""); rec.Code != 200 {
		t.Errorf("liveness must not depend on the gazetteer: %d", rec.Code)
	}
	expectProblem(t, h.do("POST", "/v1/geocode", "wrong", landmarkAddr), 401, "unauthorized") // auth still comes first

	ready = true // a build was activated: no restart
	rec := h.do("POST", "/v1/geocode", "keyA", landmarkAddr)
	if rec.Code != 200 || decodeBody(t, rec)["data_version"] != "2026-10-gaz-1" {
		t.Fatalf("after activation: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("GET", "/readyz", "", ""); rec.Code != 200 {
		t.Errorf("readyz after activation: %d", rec.Code)
	}
}
