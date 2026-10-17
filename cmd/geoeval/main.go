// Command geoeval measures the geocoder on a set of addresses.
//
//	geoeval import -set e1-lite -tier A -source "door-step GPS" -file my-addresses.csv
//	geoeval run    -set e1-lite            # geocode every case with the active gazetteer, print the report
//	geoeval report -run 7                  # show a stored run again
//
// Needs DATABASE_URL (a role that can write eval and read gaz; the database owner in development).
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/evaluate"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/gazetteer"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/resolver"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fail("DATABASE_URL is required")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		fail(err.Error())
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	norm := resolver.NewNormalizer(resolver.DefaultTerms())

	switch os.Args[1] {
	case "import":
		fs := flag.NewFlagSet("import", flag.ExitOnError)
		set := fs.String("set", "", "name for the new set")
		tier := fs.String("tier", "A", "A (ground truth collected on the ground), B (text only) or C (independent dataset)")
		source := fs.String("source", "", "where the data came from")
		desc := fs.String("description", "", "optional note")
		file := fs.String("file", "", "CSV file")
		fs.Parse(os.Args[2:])
		if *set == "" || *file == "" || *source == "" {
			fail("-set, -file and -source are required")
		}
		f, err := os.Open(*file)
		if err != nil {
			fail(err.Error())
		}
		defer f.Close()
		cases, err := evaluate.ReadCSV(f, norm)
		if err != nil {
			fail(err.Error())
		}
		n, err := evaluate.Import(ctx, db, evaluate.SetInfo{Name: *set, Tier: *tier, Source: *source, Description: *desc}, cases)
		if err != nil {
			fail(err.Error())
		}
		fmt.Printf("imported %d cases into set %q (tier %s)\n", n, *set, *tier)
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		set := fs.String("set", "", "set to evaluate")
		fs.Parse(os.Args[2:])
		if *set == "" {
			fail("-set is required")
		}
		vid, label, err := gazetteer.ActiveVersion(ctx, db)
		if err != nil {
			fail(err.Error())
		}
		pipe := resolver.NewPipeline(norm, gazetteer.NewSource(db, norm))
		sha := "unknown"
		if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
			sha = strings.TrimSpace(string(out))
		}
		cfg := fmt.Sprintf(`{"source":"gazetteer","gazetteer":%q,"thresholds":{"building":%v,"street":%v,"locality":%v,"lga":%v,"state":%v}}`, label,
			pipe.Thresholds[resolver.Building], pipe.Thresholds[resolver.Street], pipe.Thresholds[resolver.Locality], pipe.Thresholds[resolver.LGA], pipe.Thresholds[resolver.State])
		runID, err := evaluate.Run(ctx, db, pipe, evaluate.RunOptions{Set: *set, GitSHA: sha, VersionID: vid, Config: cfg})
		if err != nil {
			fail(err.Error())
		}
		show(ctx, db, runID)
	case "report":
		fs := flag.NewFlagSet("report", flag.ExitOnError)
		run := fs.String("run", "", "run id")
		fs.Parse(os.Args[2:])
		id, err := strconv.ParseInt(*run, 10, 64)
		if err != nil {
			fail("-run needs a run id")
		}
		show(ctx, db, id)
	default:
		usage()
	}
}

func show(ctx context.Context, db *sql.DB, runID int64) {
	s, err := evaluate.Report(ctx, db, runID)
	if err != nil {
		fail(err.Error())
	}
	fmt.Print(s.String())
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: geoeval import|run|report   (see the file header for flags)")
	os.Exit(2)
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "error:", msg)
	os.Exit(1)
}
