# Measuring accuracy (geoeval)

The accuracy numbers on the CV come from here. Do not tune the scoring weights before you have a
tier A set, or you will fit the weights to your guesses.

## Tiers

| Tier | What it is | Truth coordinates | What it tells you |
| --- | --- | --- | --- |
| A | Addresses you visited or got from people on the ground | yes, taken at the door | Accuracy, calibration, false-confidence rate. The CV numbers |
| B | Addresses copied from public web pages (bank branches, hospitals, schools, hotels) | no | Resolution rate, precision mix, which kinds of text fail |
| C | An independent dataset with names and coordinates (for example the GRID3 facility list) | yes, not validated | Accuracy at scale; compare sources against each other, do not publish as ground truth |

## Collecting tier A (the E1-lite set: 60 addresses, about 40 in Lagos, 20 elsewhere through contacts)

1. Write the address the way a customer would, with landmarks and all. Do not tidy it. Mix styles:
   structured ("12 Allen Avenue, Ikeja"), landmark-led ("opp. First Bank, Allen Avenue"), a bare
   place name ("Computer Village").
2. Stand at the main entrance or gate. Take the coordinates from your phone's GPS fix (the blue dot or
   a coordinates screen), wait about 30 seconds outdoors, and note both numbers: latitude first.
3. Never drag a pin onto a map and copy it, and never copy a pin from a website: that is a map's guess,
   not the truth, and it makes the test circular. The tool refuses truth_method values that mention Google.
4. Set expected_precision to what your truth really supports: `building` if you stood at the entrance of
   that specific building, `street` if the address only places someone along a road near a landmark,
   `locality` if it only names a neighbourhood.
5. Use public venues, businesses, or addresses whose owners agree. Home addresses are personal data.

Start from `testdata/e1-lite-template.csv` (delete its example row). The columns:
`raw_text, lat, lng, expected_precision, truth_method, city, state, style, collected_at, notes, source_url`.

## Running it

    DATABASE_URL=... go run ./cmd/geoeval import -set e1-lite -tier A -source "door-step GPS" -file my-addresses.csv
    DATABASE_URL=... go run ./cmd/geoeval run -set e1-lite
    DATABASE_URL=... go run ./cmd/geoeval report -run 7     # show a stored run again

The report gives resolution rate, precision mix, median error, the share within 50/100/500 m and 1 km,
calibration (of answers with confidence 0.8 or more, how many fell inside their claimed radius; the
target is 90%) and the false-confidence rate (confident answers more than 500 m off; the target is 5%
or less), then lists the largest errors so you can see which kinds of address fail.

## Reading the result

* False confidence above target means the thresholds are too generous: raise them in `DefaultThresholds`
  or lower the weights in `internal/gazetteer/source.go`, rerun, and compare runs (each run stores the
  thresholds and gazetteer version it used).
* Resolution rate low with good accuracy means the data, not the scoring, is the limit: look at the
  largest-error list and at tier B for the address styles that find nothing.
* Always report the number of cases next to a percentage. 60 addresses gives about plus or minus 11 points.
