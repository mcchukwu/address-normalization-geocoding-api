// Package store is everything the API remembers: tenants, the request log and corrections.
// Postgres implements it in production (following the app schema); Memory implements it for
// development and tests.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrDuplicate means a correction for that tenant and request already exists.
var ErrDuplicate = errors.New("duplicate")

// TenantSettings are what the geocoder itself remembers about a tenant.
type TenantSettings struct {
	TrafficClass  string // real, synthetic or loadtest: set by an operator, never by the caller
	RetainRaw     bool
	RatePerMin    *int
	GoogleEnabled bool
	NDAPSEnabled  bool
}

// RequestRecord is one row of the request log: metadata only, never raw text or coordinates.
type RequestRecord struct {
	ID            string
	ReceivedAt    time.Time
	TenantID      string
	KeyID         string
	Endpoint      string
	TrafficClass  string
	Status        int
	LatencyMS     int
	InputHash     []byte
	Precision     string // empty = not applicable
	Confidence    *float64
	StateID       int // 0 = unknown
	LGAName       string
	SourcesUsed   []string
	Warnings      []string
	DataVersionID int // 0 = unknown
	ErrorCode     string
}

// RawText is the raw address of an opted-in tenant, kept for at most 30 days.
type RawText struct {
	RequestID  string
	ReceivedAt time.Time
	Text       string
	ExpiresAt  time.Time
}

// RequestInfo is what a correction needs to know about the request it refers to.
type RequestInfo struct {
	ReceivedAt time.Time
	Precision  string
	Confidence *float64
}

// Correction is a location confirmed by a tenant.
type Correction struct {
	ID                 string
	TenantID           string
	RequestID          string
	InputHash          []byte
	InputText          string // only for tenants that opted in
	InputTextExpiresAt *time.Time
	NormalizedJSON     []byte
	OriginalPrecision  string
	OriginalConfidence *float64
	Lat, Lng           float64
	Note               string
}

// Store is the persistence the API needs.
type Store interface {
	// EnsureTenant creates the tenant on first sight and returns its settings.
	EnsureTenant(ctx context.Context, tenantID, name string) (TenantSettings, error)
	LogRequest(ctx context.Context, r RequestRecord) error
	LogRawText(ctx context.Context, r RawText) error
	// FindRequest returns the request only if it belongs to the tenant.
	FindRequest(ctx context.Context, tenantID, requestID string) (RequestInfo, bool, error)
	// InsertCorrection returns ErrDuplicate if the tenant already corrected that request.
	InsertCorrection(ctx context.Context, c Correction) (time.Time, error)
	Ping(ctx context.Context) error
	Close() error
}
