// Package probe is a black-box check of the live API: once per interval it sends one real geocode
// request the way a customer would (through the public URL, TLS and key included) and records
// whether it worked and how long it took. Its numbers are the honest source for the uptime figure,
// because they measure what a caller experiences, not what the server believes about itself.
package probe

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultAddresses is a small, varied rotation: structured, landmark-led and bare place names.
var DefaultAddresses = []string{
	"12 allen avenue, ikeja, lagos",
	"opp. first bank, allen avenue, ikeja, lagos",
	"garki, abuja",
	"victoria island, lagos",
	"adeola odeku street, victoria island",
	"computer village, ikeja",
	"zaria road, kano",
	"ikorodu road, lagos",
	"gra, port harcourt",
	"wuse 2, abuja",
}

var buckets = []float64{0.05, 0.1, 0.2, 0.3, 0.5, 0.8, 1.5, 3}

// Prober sends requests and keeps the results.
type Prober struct {
	URL       string // base URL, e.g. https://api.example.com
	Key       string
	Addresses []string
	Client    *http.Client

	mu          sync.Mutex
	i           int
	success     uint64
	failure     uint64
	counts      []uint64
	sum         float64
	lastSuccess time.Time
	lastOK      bool
	started     bool
}

// Once sends one request and records the outcome. A success is a 200 with a request_id in the body.
func (p *Prober) Once(ctx context.Context) error {
	addrs := p.Addresses
	if len(addrs) == 0 {
		addrs = DefaultAddresses
	}
	p.mu.Lock()
	addr := addrs[p.i%len(addrs)]
	p.i++
	p.mu.Unlock()

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.URL, "/")+"/v1/geocode",
		bytes.NewBufferString(fmt.Sprintf(`{"address":%q}`, addr)))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", p.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	var failure error
	if err != nil {
		failure = err
	} else {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode != http.StatusOK:
			failure = fmt.Errorf("status %d", resp.StatusCode)
		case !bytes.Contains(b, []byte(`"request_id"`)):
			failure = fmt.Errorf("200 without a request_id")
		}
	}
	d := time.Since(start)
	p.record(d, failure == nil)
	return failure
}

func (p *Prober) record(d time.Duration, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.counts == nil {
		p.counts = make([]uint64, len(buckets))
	}
	p.started, p.lastOK = true, ok
	if ok {
		p.success++
		p.lastSuccess = time.Now()
	} else {
		p.failure++
	}
	s := d.Seconds()
	for i, b := range buckets {
		if s <= b {
			p.counts[i]++
			break
		}
	}
	p.sum += s
}

// Run probes every interval until ctx ends, calling onError for each failed probe.
func (p *Prober) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := p.Once(ctx); err != nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// WritePrometheus writes the probe's metrics in the Prometheus text format.
func (p *Prober) WritePrometheus(w io.Writer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	up := 0
	if p.lastOK {
		up = 1
	}
	fmt.Fprintf(w, "# HELP probe_up 1 if the most recent probe succeeded.\n# TYPE probe_up gauge\nprobe_up %d\n", up)
	fmt.Fprintf(w, "# HELP probe_requests_total Probe requests by outcome.\n# TYPE probe_requests_total counter\n")
	fmt.Fprintf(w, "probe_requests_total{outcome=\"success\"} %d\nprobe_requests_total{outcome=\"failure\"} %d\n", p.success, p.failure)
	fmt.Fprintf(w, "# HELP probe_last_success_timestamp_seconds Unix time of the last successful probe.\n# TYPE probe_last_success_timestamp_seconds gauge\n")
	fmt.Fprintf(w, "probe_last_success_timestamp_seconds %d\n", p.lastSuccess.Unix())
	fmt.Fprintf(w, "# HELP probe_duration_seconds Probe latency.\n# TYPE probe_duration_seconds histogram\n")
	var cum uint64
	counts := p.counts
	if counts == nil {
		counts = make([]uint64, len(buckets))
	}
	for i, b := range buckets {
		cum += counts[i]
		fmt.Fprintf(w, "probe_duration_seconds_bucket{le=\"%g\"} %d\n", b, cum)
	}
	total := p.success + p.failure
	fmt.Fprintf(w, "probe_duration_seconds_bucket{le=\"+Inf\"} %d\nprobe_duration_seconds_sum %g\nprobe_duration_seconds_count %d\n", total, p.sum, total)
}
