package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
)

// Postgres implements Store on the app schema from the migrations.
type Postgres struct{ DB *sql.DB }

// OpenPostgres connects and checks the connection.
func OpenPostgres(ctx context.Context, url string) (*Postgres, error) {
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Postgres{DB: db}, nil
}

// EnsureTenant implements Store: insert on first sight, refresh the name otherwise.
func (p *Postgres) EnsureTenant(ctx context.Context, id, name string) (TenantSettings, error) {
	var t TenantSettings
	var rate sql.NullInt64
	err := p.DB.QueryRowContext(ctx, `
		INSERT INTO app.tenants (tenant_id, display_name) VALUES ($1::uuid, $2)
		ON CONFLICT (tenant_id) DO UPDATE SET display_name = EXCLUDED.display_name
		RETURNING traffic_class::text, retain_raw, rate_limit_per_min, google_enabled, ndaps_enabled`,
		id, name).Scan(&t.TrafficClass, &t.RetainRaw, &rate, &t.GoogleEnabled, &t.NDAPSEnabled)
	if rate.Valid {
		v := int(rate.Int64)
		t.RatePerMin = &v
	}
	return t, err
}

// LogRequest implements Store.
func (p *Postgres) LogRequest(ctx context.Context, r RequestRecord) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO app.requests (id, received_at, tenant_id, key_id, endpoint, traffic_class, status_code,
			latency_ms, input_hash, precision, confidence, state_id, lga_name, sources_used, warnings,
			data_version_id, error_code)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6::app.traffic_class, $7, $8, $9, $10::ref.precision, $11,
			$12, $13, $14, $15, $16, $17)`,
		r.ID, r.ReceivedAt, r.TenantID, r.KeyID, r.Endpoint, r.TrafficClass, r.Status, r.LatencyMS,
		nilBytes(r.InputHash), nullString(r.Precision), nullFloat(r.Confidence), nullInt(r.StateID),
		nullString(r.LGAName), pq.Array(nonNil(r.SourcesUsed)), pq.Array(nonNil(r.Warnings)),
		nullInt(r.DataVersionID), nullString(r.ErrorCode))
	return err
}

// LogRawText implements Store.
func (p *Postgres) LogRawText(ctx context.Context, r RawText) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO app.request_raw_text (request_id, received_at, raw_input, expires_at)
		VALUES ($1::uuid, $2, $3, $4)`, r.RequestID, r.ReceivedAt, r.Text, r.ExpiresAt)
	return err
}

// FindRequest implements Store. It reads only the columns the API role is granted.
func (p *Postgres) FindRequest(ctx context.Context, tenantID, requestID string) (RequestInfo, bool, error) {
	var info RequestInfo
	var prec sql.NullString
	var conf sql.NullFloat64
	err := p.DB.QueryRowContext(ctx, `
		SELECT received_at, precision::text, confidence FROM app.requests
		WHERE id = $1::uuid AND tenant_id = $2::uuid
		ORDER BY received_at DESC LIMIT 1`, requestID, tenantID).Scan(&info.ReceivedAt, &prec, &conf)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestInfo{}, false, nil
	}
	if err != nil {
		return RequestInfo{}, false, err
	}
	info.Precision = prec.String
	if conf.Valid {
		info.Confidence = &conf.Float64
	}
	return info, true, nil
}

// InsertCorrection implements Store.
func (p *Postgres) InsertCorrection(ctx context.Context, c Correction) (time.Time, error) {
	var created time.Time
	err := p.DB.QueryRowContext(ctx, `
		INSERT INTO app.corrections (id, tenant_id, request_id, input_hash, input_text, input_text_expires_at,
			normalized, original_precision, original_confidence, confirmed, note)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7::jsonb, $8::ref.precision, $9,
			ST_SetSRID(ST_MakePoint($10, $11), 4326), $12)
		RETURNING created_at`,
		c.ID, c.TenantID, c.RequestID, c.InputHash, nullString(c.InputText), nullTime(c.InputTextExpiresAt),
		string(c.NormalizedJSON), c.OriginalPrecision, nullFloat(c.OriginalConfidence), c.Lng, c.Lat,
		nullString(c.Note)).Scan(&created)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return time.Time{}, ErrDuplicate
	}
	return created, err
}

// Ping implements Store.
func (p *Postgres) Ping(ctx context.Context) error { return p.DB.PingContext(ctx) }

// Close implements Store.
func (p *Postgres) Close() error { return p.DB.Close() }

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
func nullInt(i int) sql.NullInt64        { return sql.NullInt64{Int64: int64(i), Valid: i != 0} }
func nullFloat(f *float64) sql.NullFloat64 {
	if f == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *f, Valid: true}
}
func nullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}
func nilBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
