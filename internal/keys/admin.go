package keys

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// Admin manages tenants and keys. It needs a role allowed to write app.tenants and app.api_keys
// (the database owner in development); the running API's role cannot do any of this.
type Admin struct{ DB *sql.DB }

// KeyInfo describes a stored key. It never contains the secret.
type KeyInfo struct {
	KeyID      string
	TenantID   string
	Tenant     string
	Name       string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// CreateParams describes the key to issue.
type CreateParams struct {
	TenantID   string // empty: create a new tenant named TenantName
	TenantName string
	KeyName    string
	Scopes     []string
	Expires    *time.Time
	// TrafficClass labels the tenant's traffic in metrics: real (default), synthetic or loadtest.
	// Set it for probe and load-test tenants so they never count as customers.
	TrafficClass string
}

// Create issues a key and returns the full text, which is the only time it is ever available.
func (a Admin) Create(ctx context.Context, p CreateParams) (full string, info KeyInfo, err error) {
	if len(p.Scopes) == 0 {
		return "", info, errors.New("at least one scope is required")
	}
	for _, s := range p.Scopes {
		if !ValidScope(s) {
			return "", info, fmt.Errorf("unknown scope %q (valid: %v)", s, Scopes)
		}
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", info, err
	}
	defer tx.Rollback()

	class := p.TrafficClass
	if class == "" {
		class = "real"
	}
	if class != "real" && class != "synthetic" && class != "loadtest" {
		return "", info, fmt.Errorf("traffic class must be real, synthetic or loadtest, got %q", class)
	}
	tenantID := p.TenantID
	if tenantID == "" {
		if p.TenantName == "" {
			return "", info, errors.New("give an existing tenant id or a name for a new tenant")
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO app.tenants (tenant_id, display_name, traffic_class) VALUES (gen_random_uuid(), $1, $2::app.traffic_class) RETURNING tenant_id::text`,
			p.TenantName, class).Scan(&tenantID); err != nil {
			return "", info, err
		}
	}
	if p.TenantID != "" && p.TrafficClass != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE app.tenants SET traffic_class = $2::app.traffic_class WHERE tenant_id = $1::uuid`, tenantID, class); err != nil {
			return "", info, err
		}
	}
	var tenantName string
	if err := tx.QueryRowContext(ctx, `SELECT display_name FROM app.tenants WHERE tenant_id = $1::uuid`, tenantID).Scan(&tenantName); err != nil {
		return "", info, fmt.Errorf("tenant %s: %w", tenantID, err)
	}

	full, keyID, hash, err := Generate()
	if err != nil {
		return "", info, err
	}
	info = KeyInfo{KeyID: keyID, TenantID: tenantID, Tenant: tenantName, Name: p.KeyName, Scopes: p.Scopes}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO app.api_keys (id, tenant_id, key_id, secret_hash, name, scopes, expires_at)
		VALUES (gen_random_uuid(), $1::uuid, $2, $3, $4, $5, $6) RETURNING created_at`,
		tenantID, keyID, hash[:], p.KeyName, pq.Array(p.Scopes), p.Expires).Scan(&info.CreatedAt)
	if err != nil {
		return "", info, err
	}
	info.ExpiresAt = p.Expires
	return full, info, tx.Commit()
}

// Revoke disables a key immediately in the database; the API stops accepting it within its
// one-minute cache window.
func (a Admin) Revoke(ctx context.Context, keyID string) error {
	res, err := a.DB.ExecContext(ctx, `UPDATE app.api_keys SET revoked_at = now() WHERE key_id = $1 AND revoked_at IS NULL`, keyID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no active key %q", keyID)
	}
	return nil
}

// List returns keys, newest first; tenantID filters when non-empty.
func (a Admin) List(ctx context.Context, tenantID string) ([]KeyInfo, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT k.key_id, k.tenant_id::text, t.display_name, k.name, k.scopes, k.created_at, k.expires_at, k.revoked_at, k.last_used_at
		FROM app.api_keys k JOIN app.tenants t ON t.tenant_id = k.tenant_id
		WHERE ($1 = '' OR k.tenant_id = $1::uuid) ORDER BY k.created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyInfo
	for rows.Next() {
		var k KeyInfo
		var exp, rev, used sql.NullTime
		if err := rows.Scan(&k.KeyID, &k.TenantID, &k.Tenant, &k.Name, pq.Array(&k.Scopes), &k.CreatedAt, &exp, &rev, &used); err != nil {
			return nil, err
		}
		if exp.Valid {
			k.ExpiresAt = &exp.Time
		}
		if rev.Valid {
			k.RevokedAt = &rev.Time
		}
		if used.Valid {
			k.LastUsedAt = &used.Time
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// UsageRow is one line of daily usage.
type UsageRow struct {
	Tenant                              string
	Day                                 time.Time
	Endpoint                            string
	Requests, ServerErrors, RateLimited int
}

// Usage reports the last `days` days of traffic per tenant and endpoint, from the request log.
func (a Admin) Usage(ctx context.Context, days int) ([]UsageRow, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT t.display_name, u.day, u.endpoint, sum(u.requests)::int, sum(u.server_errors)::int, sum(u.rate_limited)::int
		FROM app.usage_daily u JOIN app.tenants t ON t.tenant_id = u.tenant_id
		WHERE u.day >= (now() AT TIME ZONE 'UTC')::date - $1::int AND u.traffic_class = 'real'
		GROUP BY 1, 2, 3 ORDER BY 2 DESC, 1, 3`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.Tenant, &r.Day, &r.Endpoint, &r.Requests, &r.ServerErrors, &r.RateLimited); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
