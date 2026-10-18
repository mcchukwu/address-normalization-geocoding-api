package keys

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/auth"
)

func TestGenerateAndParse(t *testing.T) {
	full, id, hash, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 76 || !strings.HasPrefix(full, "gk_"+id+"_") {
		t.Errorf("unexpected key shape: %q (id %q)", full, id)
	}
	got, err := Parse(full)
	if err != nil || got != id {
		t.Errorf("Parse: %q %v", got, err)
	}
	if Hash(full) != hash {
		t.Errorf("hash must be deterministic")
	}
	full2, id2, _, _ := Generate()
	if full == full2 || id == id2 {
		t.Errorf("two keys must differ")
	}
}

func TestParseRejectsAnythingElse(t *testing.T) {
	full, _, _, _ := Generate()
	bad := []string{
		"", "devkey", full[:len(full)-1], full + "0", strings.ToUpper(full),
		"xx_" + full[3:], strings.Replace(full, "_", "-", 1),
		full[:12] + "g" + full[13:], // a non-hex character in the secret
	}
	for _, b := range bad {
		if _, err := Parse(b); err == nil {
			t.Errorf("Parse(%q) should fail", b)
		}
	}
}

func TestValidScope(t *testing.T) {
	if !ValidScope("geocode") || ValidScope("admin") || ValidScope("") {
		t.Errorf("scope list wrong")
	}
}

// ---- database tests: need migrations applied; run as the restricted API role for the checks.

func dbs(t *testing.T) (admin, api *sql.DB) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	admin, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	var reg sql.NullString
	if err := admin.QueryRow(`SELECT to_regclass('app.api_keys')::text`).Scan(&reg); err != nil || !reg.Valid {
		t.Skip("migration 000008 not applied")
	}
	api, _ = sql.Open("postgres", url)
	api.SetMaxOpenConns(1)
	if _, err := api.Exec(`SET ROLE geocode_api`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(); api.Close() })
	return admin, api
}

func TestIssueCheckRevokeAgainstTheDatabase(t *testing.T) {
	admin, api := dbs(t)
	ctx := context.Background()
	a := Admin{DB: admin}
	local := Local{DB: api} // the API's own restricted connection

	suffix := time.Now().Format("150405.000")
	full, info, err := a.Create(ctx, CreateParams{TenantName: "Keys Test " + suffix, KeyName: "prod", Scopes: []string{"geocode", "normalize"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(`DELETE FROM app.api_keys WHERE tenant_id = $1::uuid`, info.TenantID)
		admin.Exec(`DELETE FROM app.tenants WHERE tenant_id = $1::uuid`, info.TenantID)
	})

	p, err := local.Introspect(ctx, full)
	if err != nil {
		t.Fatalf("a freshly issued key must work: %v", err)
	}
	if p.TenantID != info.TenantID || p.KeyID != info.KeyID || !p.HasScope("geocode") || !p.HasScope("normalize") || p.HasScope("corrections:write") {
		t.Errorf("principal: %+v", p)
	}
	if !strings.HasPrefix(p.TenantName, "Keys Test") {
		t.Errorf("tenant name: %q", p.TenantName)
	}

	var stored []byte
	admin.QueryRow(`SELECT secret_hash FROM app.api_keys WHERE key_id = $1`, info.KeyID).Scan(&stored)
	h := Hash(full)
	if string(stored) != string(h[:]) || strings.Contains(string(stored), full[13:]) {
		t.Errorf("only the hash may be stored")
	}
	var used sql.NullTime
	admin.QueryRow(`SELECT last_used_at FROM app.api_keys WHERE key_id = $1`, info.KeyID).Scan(&used)
	if !used.Valid {
		t.Errorf("a successful check should stamp last_used_at")
	}

	// wrong secret for a real key id, unknown id, malformed: all the same error
	wrong := full[:13] + strings.Repeat("0", 64)
	for _, bad := range []string{wrong, "gk_deadbeef_" + strings.Repeat("a", 64), "devkey"} {
		if _, err := local.Introspect(ctx, bad); !errors.Is(err, auth.ErrInvalidKey) {
			t.Errorf("%q should be ErrInvalidKey, got %v", bad[:8], err)
		}
	}

	// the restricted role can check keys but cannot create, change or read more than it needs
	if _, err := api.Exec(`INSERT INTO app.api_keys (id, tenant_id, key_id, secret_hash, name, scopes)
		VALUES (gen_random_uuid(), $1::uuid, '00000000', $2, 'x', '{geocode}')`, info.TenantID, h[:]); err == nil {
		t.Errorf("the API role must not be able to create keys")
	}
	if _, err := api.Exec(`UPDATE app.api_keys SET scopes = '{geocode,normalize,corrections:write}' WHERE key_id = $1`, info.KeyID); err == nil {
		t.Errorf("the API role must not be able to widen a key's scopes")
	}
	if _, err := api.Exec(`SELECT name FROM app.api_keys`); err == nil {
		t.Errorf("the API role should not read columns it does not need")
	}

	// revoke
	if err := a.Revoke(ctx, info.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Introspect(ctx, full); !errors.Is(err, auth.ErrInvalidKey) {
		t.Errorf("a revoked key must be refused, got %v", err)
	}
	if err := a.Revoke(ctx, info.KeyID); err == nil {
		t.Errorf("revoking twice should say there is no active key")
	}
}

func TestExpiredKeysAndBadInput(t *testing.T) {
	admin, api := dbs(t)
	ctx := context.Background()
	a := Admin{DB: admin}

	past := time.Now().Add(-time.Minute)
	full, info, err := a.Create(ctx, CreateParams{TenantName: "Expired " + time.Now().Format("150405.000"), KeyName: "old", Scopes: []string{"geocode"}, Expires: &past})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(`DELETE FROM app.api_keys WHERE tenant_id = $1::uuid`, info.TenantID)
		admin.Exec(`DELETE FROM app.tenants WHERE tenant_id = $1::uuid`, info.TenantID)
	})
	if _, err := (Local{DB: api}).Introspect(ctx, full); !errors.Is(err, auth.ErrInvalidKey) {
		t.Errorf("an expired key must be refused, got %v", err)
	}

	if _, _, err := a.Create(ctx, CreateParams{TenantName: "x", KeyName: "k", Scopes: []string{"admin"}}); err == nil {
		t.Errorf("unknown scope must be refused")
	}
	if _, _, err := a.Create(ctx, CreateParams{TenantName: "x", KeyName: "k"}); err == nil {
		t.Errorf("a key with no scopes must be refused")
	}
	// the database enforces the same rules even if the Go check is bypassed
	_, err = admin.Exec(`INSERT INTO app.api_keys (id, tenant_id, key_id, secret_hash, name, scopes)
		VALUES (gen_random_uuid(), $1::uuid, 'abcdef01', '\x00', 'x', '{geocode}')`, info.TenantID)
	if err == nil {
		t.Errorf("a secret hash of the wrong length must be refused by the database")
	}
	_, err = admin.Exec(`INSERT INTO app.api_keys (id, tenant_id, key_id, secret_hash, name, scopes)
		VALUES (gen_random_uuid(), $1::uuid, 'abcdef02', $2, 'x', '{admin}')`, info.TenantID, make([]byte, 32))
	if err == nil {
		t.Errorf("an unknown scope must be refused by the database")
	}
}

func TestTrafficClassIsSetWhenIssuingAKey(t *testing.T) {
	admin, _ := dbs(t)
	ctx := context.Background()
	a := Admin{DB: admin}
	_, info, err := a.Create(ctx, CreateParams{TenantName: "Probe " + time.Now().Format("150405.000"), KeyName: "probe", Scopes: []string{"geocode"}, TrafficClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(`DELETE FROM app.api_keys WHERE tenant_id = $1::uuid`, info.TenantID)
		admin.Exec(`DELETE FROM app.tenants WHERE tenant_id = $1::uuid`, info.TenantID)
	})
	var class string
	admin.QueryRow(`SELECT traffic_class::text FROM app.tenants WHERE tenant_id = $1::uuid`, info.TenantID).Scan(&class)
	if class != "synthetic" {
		t.Errorf("tenant class: %q", class)
	}
	// a second key for the same tenant can change the class; an invalid class is refused
	if _, _, err := a.Create(ctx, CreateParams{TenantID: info.TenantID, KeyName: "again", Scopes: []string{"geocode"}, TrafficClass: "loadtest"}); err != nil {
		t.Fatal(err)
	}
	admin.QueryRow(`SELECT traffic_class::text FROM app.tenants WHERE tenant_id = $1::uuid`, info.TenantID).Scan(&class)
	if class != "loadtest" {
		t.Errorf("class should have been updated: %q", class)
	}
	if _, _, err := a.Create(ctx, CreateParams{TenantName: "x", KeyName: "k", Scopes: []string{"geocode"}, TrafficClass: "vip"}); err == nil {
		t.Errorf("an unknown traffic class must be refused")
	}
}
