package keys

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/auth"
)

// Local checks keys against app.api_keys. It implements auth.Introspector, so the API wraps it in
// the same short cache as any other key source: the database is asked about a key about once a minute.
type Local struct{ DB *sql.DB }

// Introspect implements auth.Introspector. Every failure (malformed, unknown, wrong secret, revoked,
// expired) returns the same ErrInvalidKey, so a caller learns nothing about which part was wrong.
func (l Local) Introspect(ctx context.Context, key string) (auth.Principal, error) {
	keyID, err := Parse(key)
	if err != nil {
		return auth.Principal{}, auth.ErrInvalidKey
	}

	var (
		id, tenantID, name string
		hash               []byte
		scopes             []string
		expires, revoked   sql.NullTime
	)
	err = l.DB.QueryRowContext(ctx, `
		SELECT k.id::text, k.tenant_id::text, t.display_name, k.secret_hash, k.scopes, k.expires_at, k.revoked_at
		FROM app.api_keys k JOIN app.tenants t ON t.tenant_id = k.tenant_id
		WHERE k.key_id = $1`, keyID).Scan(&id, &tenantID, &name, &hash, pq.Array(&scopes), &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		// compare against a dummy so unknown ids and wrong secrets take the same time
		got := Hash(key)
		subtle.ConstantTimeCompare(got[:], make([]byte, 32))
		return auth.Principal{}, auth.ErrInvalidKey
	}
	if err != nil {
		return auth.Principal{}, errors.Join(auth.ErrUnavailable, err)
	}

	got := Hash(key)
	if subtle.ConstantTimeCompare(got[:], hash) != 1 {
		return auth.Principal{}, auth.ErrInvalidKey
	}
	now := time.Now()
	if revoked.Valid || (expires.Valid && !expires.Time.After(now)) {
		return auth.Principal{}, auth.ErrInvalidKey
	}

	// Best effort and bounded: stamping last use must never slow down or fail a request.
	stamp, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, _ = l.DB.ExecContext(stamp, `UPDATE app.api_keys SET last_used_at = now() WHERE id = $1::uuid`, id)

	return auth.Principal{TenantID: tenantID, TenantName: name, KeyID: keyID, Scopes: scopes}, nil
}
