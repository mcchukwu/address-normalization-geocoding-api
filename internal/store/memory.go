package store

import (
	"context"
	"sync"
	"time"
)

// Memory is an in-process Store for development and tests.
type Memory struct {
	mu          sync.Mutex
	Tenants     map[string]TenantSettings
	Requests    []RequestRecord
	RawTexts    []RawText
	Corrections []Correction
	// Defaults applied to tenants created on first sight.
	Fail error // when set, every call returns it (to test degradation)
}

// NewMemory returns an empty Memory store.
func NewMemory() *Memory { return &Memory{Tenants: map[string]TenantSettings{}} }

// EnsureTenant implements Store.
func (m *Memory) EnsureTenant(_ context.Context, id, _ string) (TenantSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return TenantSettings{}, m.Fail
	}
	t, ok := m.Tenants[id]
	if !ok {
		t = TenantSettings{TrafficClass: "real"}
		m.Tenants[id] = t
	}
	return t, nil
}

// LogRequest implements Store.
func (m *Memory) LogRequest(_ context.Context, r RequestRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	m.Requests = append(m.Requests, r)
	return nil
}

// LogRawText implements Store.
func (m *Memory) LogRawText(_ context.Context, r RawText) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RawTexts = append(m.RawTexts, r)
	return nil
}

// FindRequest implements Store.
func (m *Memory) FindRequest(_ context.Context, tenantID, requestID string) (RequestInfo, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.Requests {
		if r.ID == requestID && r.TenantID == tenantID {
			return RequestInfo{ReceivedAt: r.ReceivedAt, Precision: r.Precision, Confidence: r.Confidence}, true, nil
		}
	}
	return RequestInfo{}, false, nil
}

// InsertCorrection implements Store.
func (m *Memory) InsertCorrection(_ context.Context, c Correction) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.Corrections {
		if x.TenantID == c.TenantID && x.RequestID == c.RequestID {
			return time.Time{}, ErrDuplicate
		}
	}
	m.Corrections = append(m.Corrections, c)
	return time.Now().UTC(), nil
}

// Ping implements Store.
func (m *Memory) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Fail
}

// Close implements Store.
func (m *Memory) Close() error { return nil }
