// Command gazetteer builds a new gazetteer version from staged OpenStreetMap data and boundaries.
//
//	osm2pgsql -d "$DATABASE_URL" -O flex -S scripts/osm2pgsql-gazetteer.lua nigeria-latest.osm.pbf
//	go run ./cmd/gazetteer -adm1 data/boundaries/geoBoundaries-NGA-ADM1.geojson \
//	                       -adm2 data/boundaries/geoBoundaries-NGA-ADM2.geojson -activate
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	_ "github.com/lib/pq"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/gazetteer"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
)

func main() {
	adm1 := flag.String("adm1", "data/boundaries/geoBoundaries-NGA-ADM1.geojson", "state boundaries (GeoJSON)")
	adm2 := flag.String("adm2", "data/boundaries/geoBoundaries-NGA-ADM2.geojson", "LGA boundaries (GeoJSON)")
	label := flag.String("label", time.Now().UTC().Format("2006-01-02")+"-gaz", "label for this build")
	activate := flag.Bool("activate", false, "make this build the one the API reads")
	notes := flag.String("notes", "", "free-text note stored with the build")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Error("DATABASE_URL is required (a role that can write the gaz schema and read osm_stage)")
		os.Exit(1)
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		log.Error("open", "err", err.Error())
		os.Exit(1)
	}
	defer db.Close()

	rep, err := gazetteer.Build(context.Background(), db, gazetteer.Options{
		Label: *label, ADM1Path: *adm1, ADM2Path: *adm2, Norm: resolver.NewNormalizer(resolver.DefaultTerms()),
		Activate: *activate, Notes: *notes, Log: log,
	})
	if err != nil {
		log.Error("build failed; nothing was kept", "err", err.Error())
		os.Exit(1)
	}
	fmt.Printf("built %s (version %d) in %s\n", rep.Label, rep.VersionID, rep.Elapsed.Round(time.Millisecond))
	kinds := make([]string, 0, len(rep.Counts))
	for k := range rep.Counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("  %-9s %d\n", k, rep.Counts[k])
	}
	fmt.Printf("  names     %d\n  outside every LGA: %d\n  active: %v\n", rep.Names, rep.Unassigned, rep.Activated)
}
