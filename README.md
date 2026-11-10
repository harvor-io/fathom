# fathom

A CLI tool for measuring how services scale under load, fitting Universal Scalability Law (USL) curves, and generating reproducible capacity reports.

## Usage

```sh
go build .

# Measure a URL's throughput at each concurrency level (10s each, 3 sweeps by default)
# and estimate how it scales
./fathom run --url http://localhost:8080/ --concurrency 1,2,4,8,16,32,64 --duration 10s --repeat 3

# Estimate scaling from measurements taken some other way, or from a saved run
./fathom run --url http://localhost:8080/ --format csv --output run.csv
./fathom fit run.csv --format json
```

The report gives the USL model, the peak, measured versus predicted throughput with its spread
across repeats, scaling efficiency, and projections beyond the measured range. Output formats:
`text` (default), `json`, `csv`.

The estimate is only as good as where you measure: include concurrency 1 (single-worker
throughput is the USL's λ), use several distinct levels, repeat them to see noise, and measure
past the peak if you want to know where it is. `fathom run` does that last part for you: while
the fitted peak is beyond the largest level measured, it doubles that level, measures it and
refits, up to `--max-concurrency` (default 1024). If it stops before the peak, the report notes
that the peak is extrapolated.
