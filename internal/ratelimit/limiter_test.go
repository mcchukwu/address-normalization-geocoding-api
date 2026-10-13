package ratelimit

import (
	"testing"
	"time"
)

func TestBucketEmptiesThenRefills(t *testing.T) {
	now := time.Unix(0, 0)
	l := New()
	l.Now = func() time.Time { return now }

	for i := 0; i < 60; i++ {
		if d := l.Allow("t", 60, 1); !d.Allowed {
			t.Fatalf("request %d should pass", i+1)
		}
	}
	d := l.Allow("t", 60, 1)
	if d.Allowed || d.RetryAfterSeconds != 1 || d.Remaining != 0 {
		t.Errorf("61st request should be refused with a 1 s wait: %+v", d)
	}
	now = now.Add(2 * time.Second) // 60/min refills one token a second
	if d := l.Allow("t", 60, 1); !d.Allowed {
		t.Errorf("a token should have refilled: %+v", d)
	}
}

func TestTenantsDoNotShareBuckets(t *testing.T) {
	l := New()
	for i := 0; i < 60; i++ {
		l.Allow("a", 60, 1)
	}
	if d := l.Allow("a", 60, 1); d.Allowed {
		t.Errorf("tenant a should be limited")
	}
	if d := l.Allow("b", 60, 1); !d.Allowed {
		t.Errorf("tenant b must be unaffected")
	}
}

func TestBatchCostsMore(t *testing.T) {
	l := New()
	d := l.Allow("t", 60, 11) // a 100-address batch costs 1 + 100/10
	if !d.Allowed || d.Remaining != 49 {
		t.Errorf("batch should take 11 tokens: %+v", d)
	}
	if d := l.Allow("t", 60, 500); !d.Allowed && d.Limit != 60 {
		t.Errorf("an oversized cost is capped, not impossible: %+v", d)
	}
}

func TestHeadersReflectState(t *testing.T) {
	l := New()
	d := l.Allow("t", 120, 1)
	if d.Limit != 120 || d.Remaining != 119 || d.ResetSeconds < 1 {
		t.Errorf("unexpected decision %+v", d)
	}
}

func TestSweepForgetsIdleTenants(t *testing.T) {
	now := time.Unix(0, 0)
	l := New()
	l.Now = func() time.Time { return now }
	l.Allow("old", 60, 1)
	now = now.Add(time.Hour)
	l.Allow("fresh", 60, 1)
	if n := l.Sweep(30 * time.Minute); n != 1 {
		t.Errorf("want 1 swept, got %d", n)
	}
}
