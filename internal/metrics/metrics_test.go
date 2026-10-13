package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestExposition(t *testing.T) {
	m := New()
	m.ObserveRequest("geocode", 200, "real", 40*time.Millisecond)
	m.ObserveRequest("geocode", 200, "real", 400*time.Millisecond)
	m.ObserveRequest("geocode", 429, "real", time.Millisecond)
	m.ObserveResult("street", "real")
	m.ObserveSource("fixture", "ok")
	m.LogDropped()

	var b strings.Builder
	m.WritePrometheus(&b)
	out := b.String()
	for _, want := range []string{
		`geocode_http_requests_total{endpoint="geocode",status_class="2xx",traffic_class="real"} 2`,
		`geocode_http_requests_total{endpoint="geocode",status_class="4xx",traffic_class="real"} 1`,
		`geocode_http_request_duration_seconds_bucket{endpoint="geocode",traffic_class="real",le="0.05"} 2`,
		`geocode_http_request_duration_seconds_bucket{endpoint="geocode",traffic_class="real",le="0.5"} 3`,
		`geocode_http_request_duration_seconds_bucket{endpoint="geocode",traffic_class="real",le="+Inf"} 3`,
		`geocode_http_request_duration_seconds_count{endpoint="geocode",traffic_class="real"} 3`,
		`geocode_results_total{precision="street",traffic_class="real"} 1`,
		`geocode_source_calls_total{source="fixture",outcome="ok"} 1`,
		`geocode_request_log_dropped_total 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing line: %s\n---\n%s", want, out)
		}
	}
}
