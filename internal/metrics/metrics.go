// Package metrics keeps the few counters and histograms the dashboard needs and writes them in
// the Prometheus text format. Every label has a small fixed set of values, so cardinality is bounded.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Buckets are placed around the 300 ms and 1.5 s latency targets (seconds).
var Buckets = []float64{0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 0.8, 1.5, 3}

type hist struct {
	counts []uint64 // cumulative per bucket is computed on output
	sum    float64
	n      uint64
}

// Metrics is safe for concurrent use.
type Metrics struct {
	mu         sync.Mutex
	requests   map[string]uint64 // endpoint|status_class|traffic_class
	latency    map[string]*hist  // endpoint|traffic_class
	results    map[string]uint64 // precision|traffic_class
	sources    map[string]uint64 // source|outcome
	logDropped atomic.Uint64
}

// New returns an empty Metrics.
func New() *Metrics {
	return &Metrics{requests: map[string]uint64{}, latency: map[string]*hist{}, results: map[string]uint64{}, sources: map[string]uint64{}}
}

// ObserveRequest records one finished HTTP request.
func (m *Metrics) ObserveRequest(endpoint string, status int, trafficClass string, d time.Duration) {
	class := fmt.Sprintf("%dxx", status/100)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[endpoint+"|"+class+"|"+trafficClass]++
	key := endpoint + "|" + trafficClass
	h := m.latency[key]
	if h == nil {
		h = &hist{counts: make([]uint64, len(Buckets))}
		m.latency[key] = h
	}
	s := d.Seconds()
	for i, b := range Buckets {
		if s <= b {
			h.counts[i]++
			break
		}
	}
	h.sum += s
	h.n++
}

// ObserveResult records the precision class of one geocode answer.
func (m *Metrics) ObserveResult(precision, trafficClass string) {
	m.mu.Lock()
	m.results[precision+"|"+trafficClass]++
	m.mu.Unlock()
}

// ObserveSource records whether a source answered ("ok") or failed ("error").
func (m *Metrics) ObserveSource(source, outcome string) {
	m.mu.Lock()
	m.sources[source+"|"+outcome]++
	m.mu.Unlock()
}

// LogDropped counts request-log rows dropped because the writer queue was full.
func (m *Metrics) LogDropped() { m.logDropped.Add(1) }

// WritePrometheus writes the Prometheus text exposition format.
func (m *Metrics) WritePrometheus(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Fprintln(w, "# HELP geocode_http_requests_total HTTP requests by endpoint, status class and traffic class.")
	fmt.Fprintln(w, "# TYPE geocode_http_requests_total counter")
	for _, k := range sortedKeys(m.requests) {
		p := strings.Split(k, "|")
		fmt.Fprintf(w, "geocode_http_requests_total{endpoint=%q,status_class=%q,traffic_class=%q} %d\n", p[0], p[1], p[2], m.requests[k])
	}

	fmt.Fprintln(w, "# HELP geocode_http_request_duration_seconds Request latency by endpoint and traffic class.")
	fmt.Fprintln(w, "# TYPE geocode_http_request_duration_seconds histogram")
	hkeys := make([]string, 0, len(m.latency))
	for k := range m.latency {
		hkeys = append(hkeys, k)
	}
	sort.Strings(hkeys)
	for _, k := range hkeys {
		p := strings.Split(k, "|")
		h := m.latency[k]
		var cum uint64
		for i, b := range Buckets {
			cum += h.counts[i]
			fmt.Fprintf(w, "geocode_http_request_duration_seconds_bucket{endpoint=%q,traffic_class=%q,le=\"%g\"} %d\n", p[0], p[1], b, cum)
		}
		fmt.Fprintf(w, "geocode_http_request_duration_seconds_bucket{endpoint=%q,traffic_class=%q,le=\"+Inf\"} %d\n", p[0], p[1], h.n)
		fmt.Fprintf(w, "geocode_http_request_duration_seconds_sum{endpoint=%q,traffic_class=%q} %g\n", p[0], p[1], h.sum)
		fmt.Fprintf(w, "geocode_http_request_duration_seconds_count{endpoint=%q,traffic_class=%q} %d\n", p[0], p[1], h.n)
	}

	fmt.Fprintln(w, "# HELP geocode_results_total Geocode answers by precision class and traffic class.")
	fmt.Fprintln(w, "# TYPE geocode_results_total counter")
	for _, k := range sortedKeys(m.results) {
		p := strings.Split(k, "|")
		fmt.Fprintf(w, "geocode_results_total{precision=%q,traffic_class=%q} %d\n", p[0], p[1], m.results[k])
	}

	fmt.Fprintln(w, "# HELP geocode_source_calls_total Source calls by source and outcome.")
	fmt.Fprintln(w, "# TYPE geocode_source_calls_total counter")
	for _, k := range sortedKeys(m.sources) {
		p := strings.Split(k, "|")
		fmt.Fprintf(w, "geocode_source_calls_total{source=%q,outcome=%q} %d\n", p[0], p[1], m.sources[k])
	}

	fmt.Fprintln(w, "# HELP geocode_request_log_dropped_total Request-log rows dropped because the queue was full.")
	fmt.Fprintln(w, "# TYPE geocode_request_log_dropped_total counter")
	fmt.Fprintf(w, "geocode_request_log_dropped_total %d\n", m.logDropped.Load())
}

func sortedKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
