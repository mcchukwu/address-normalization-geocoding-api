// Command keys manages tenants and API keys. Run it with the database owner's connection string.
//
//	keys create -tenant-name "Acme Logistics" -name production -scopes geocode,normalize
//	keys create -tenant-id <uuid> -name staging -scopes geocode -expires 30d
//	keys list [-tenant-id <uuid>]
//	keys revoke <key_id>
//	keys usage [-days 7]
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	_ "github.com/lib/pq"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/keys"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fail("DATABASE_URL is required (the database owner, not the API's restricted role)")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		fail(err.Error())
	}
	defer db.Close()
	admin := keys.Admin{DB: db}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch os.Args[1] {
	case "create":
		fs := flag.NewFlagSet("create", flag.ExitOnError)
		tenantID := fs.String("tenant-id", "", "existing tenant id")
		tenantName := fs.String("tenant-name", "", "name for a new tenant")
		name := fs.String("name", "", "a label for this key, e.g. production")
		scopes := fs.String("scopes", "geocode", "comma-separated: "+strings.Join(keys.Scopes, ", "))
		expires := fs.String("expires", "", "optional lifetime such as 30d or 12h")
		class := fs.String("traffic-class", "", "real (default), synthetic or loadtest: use synthetic for the probe so it never counts as a customer")
		fs.Parse(os.Args[2:])
		if *name == "" {
			fail("-name is required")
		}
		p := keys.CreateParams{TenantID: *tenantID, TenantName: *tenantName, KeyName: *name, Scopes: strings.Split(*scopes, ","), TrafficClass: *class}
		if *expires != "" {
			d, err := parseLifetime(*expires)
			if err != nil {
				fail(err.Error())
			}
			t := time.Now().Add(d)
			p.Expires = &t
		}
		full, info, err := admin.Create(ctx, p)
		if err != nil {
			fail(err.Error())
		}
		fmt.Printf("Tenant:  %s (%s)\nKey id:  %s\nScopes:  %s\n\nAPI key (shown once, store it now):\n\n  %s\n\n", info.Tenant, info.TenantID, info.KeyID, strings.Join(info.Scopes, ","), full)
	case "list":
		fs := flag.NewFlagSet("list", flag.ExitOnError)
		tenantID := fs.String("tenant-id", "", "only this tenant")
		fs.Parse(os.Args[2:])
		list, err := admin.List(ctx, *tenantID)
		if err != nil {
			fail(err.Error())
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "KEY ID\tTENANT\tNAME\tSCOPES\tSTATE\tLAST USED")
		for _, k := range list {
			state := "active"
			switch {
			case k.RevokedAt != nil:
				state = "revoked"
			case k.ExpiresAt != nil && !k.ExpiresAt.After(time.Now()):
				state = "expired"
			}
			last := "never"
			if k.LastUsedAt != nil {
				last = k.LastUsedAt.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", k.KeyID, k.Tenant, k.Name, strings.Join(k.Scopes, ","), state, last)
		}
		w.Flush()
	case "revoke":
		if len(os.Args) != 3 {
			fail("usage: keys revoke <key_id>")
		}
		if err := admin.Revoke(ctx, os.Args[2]); err != nil {
			fail(err.Error())
		}
		fmt.Println("revoked; the API stops accepting it within one minute")
	case "usage":
		fs := flag.NewFlagSet("usage", flag.ExitOnError)
		days := fs.Int("days", 7, "how many days back")
		fs.Parse(os.Args[2:])
		rows, err := admin.Usage(ctx, *days)
		if err != nil {
			fail(err.Error())
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "DAY\tTENANT\tENDPOINT\tREQUESTS\t5XX\t429")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\n", r.Day.Format("2006-01-02"), r.Tenant, r.Endpoint, r.Requests, r.ServerErrors, r.RateLimited)
		}
		w.Flush()
	default:
		usage()
	}
}

func parseLifetime(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n < 1 {
			return 0, fmt.Errorf("bad lifetime %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("bad lifetime %q (use 30d or 12h)", s)
	}
	return d, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: keys create|list|revoke|usage   (see the file header for flags)")
	os.Exit(2)
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "error:", msg)
	os.Exit(1)
}
