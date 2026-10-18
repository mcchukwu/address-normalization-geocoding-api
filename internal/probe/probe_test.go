package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeRecordsSuccessAndFailure(t *testing.T) {
	var mode atomic.Int32 // 0 ok, 1 server error, 2 200 with a junk body
	var seenKey, seenBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenKey.Store(r.Header.Get("X-API-Key"))
		buf := make([]byte, 200)
		n, _ := r.Body.Read(buf)
		seenBody.Store(string(buf[:n]))
		switch mode.Load() {
		case 1:
			w.WriteHeader(500)
		case 2:
			w.Write([]byte(`{"hello":"world"}`))
		default:
			w.Write([]byte(`{"request_id":"abc","result":null}`))
		}
	}))
	defer srv.Close()
	p := &Prober{URL: srv.URL + "/", Key: "gk_test", Addresses: []string{"allen avenue, ikeja", "garki, abuja"}}

	if err := p.Once(context.Background()); err != nil {
		t.Fatalf("healthy server should pass: %v", err)
	}
	if seenKey.Load() != "gk_test" || !strings.Contains(seenBody.Load().(string), "allen avenue, ikeja") {
		t.Errorf("request: key=%v body=%v", seenKey.Load(), seenBody.Load())
	}
	p.Once(context.Background())
	if !strings.Contains(seenBody.Load().(string), "garki") {
		t.Errorf("addresses should rotate: %v", seenBody.Load())
	}
	mode.Store(1)
	if err := p.Once(context.Background()); err == nil {
		t.Errorf("a 500 is a failed probe")
	}
	mode.Store(2)
	if err := p.Once(context.Background()); err == nil {
		t.Errorf("a 200 without a request_id is a failed probe")
	}
	srv.Close()
	if err := p.Once(context.Background()); err == nil {
		t.Errorf("an unreachable server is a failed probe")
	}

	var b strings.Builder
	p.WritePrometheus(&b)
	out := b.String()
	for _, want := range []string{`probe_requests_total{outcome="success"} 2`, `probe_requests_total{outcome="failure"} 3`,
		"probe_up 0", `probe_duration_seconds_count 5`, `probe_duration_seconds_bucket{le="+Inf"} 5`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRunStopsWhenContextEnds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"request_id":"x"}`)) }))
	defer srv.Close()
	p := &Prober{URL: srv.URL, Key: "k"}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { p.Run(ctx, 20*time.Millisecond, nil); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run should return when the context ends")
	}
	var b strings.Builder
	p.WritePrometheus(&b)
	if !strings.Contains(b.String(), "probe_up 1") {
		t.Errorf("probe should be up:\n%s", b.String())
	}
}
