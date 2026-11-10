package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/harvor-io/fathom/internal/loadtest"
	"github.com/harvor-io/fathom/internal/report"
	"github.com/urfave/cli/v3"
)

func runCommand() *cli.Command {
	return &cli.Command{
		Name:  "run",
		Usage: "measure a URL's throughput at each concurrency level and estimate how it scales",
		Flags: append([]cli.Flag{
			&cli.StringFlag{
				Name:     "url",
				Aliases:  []string{"u"},
				Usage:    "URL to send GET requests to",
				Required: true,
			},
			&cli.IntSliceFlag{
				Name:    "concurrency",
				Aliases: []string{"c"},
				Usage:   "concurrency levels to measure; must include 1",
				Value:   []int{1, 2, 4, 8, 16, 32},
			},
			&cli.IntFlag{
				Name:  "max-concurrency",
				Usage: "while the peak is beyond the measured range, keep doubling the largest level up to this; set it to your largest level to turn that off",
				Value: 1024,
			},
			&cli.IntFlag{
				Name:    "repeat",
				Aliases: []string{"r"},
				Usage:   "how many times to sweep all levels, to see measurement noise",
				Value:   3,
			},
			&cli.DurationFlag{
				Name:    "duration",
				Aliases: []string{"d"},
				Usage:   "how long to run each concurrency level",
				Value:   10 * time.Second,
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "per-request timeout",
				Value: 10 * time.Second,
			},
		}, outputFlags()...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			levels := slices.Clone(cmd.IntSlice("concurrency"))
			slices.Sort(levels)
			levels = slices.Compact(levels)
			if !slices.Contains(levels, 1) || len(levels) < 3 || levels[0] < 1 {
				return errors.New("--concurrency needs at least 3 distinct positive levels, including 1")
			}

			cfg := loadtest.Config{
				URL:      cmd.String("url"),
				Levels:   levels,
				Repeats:  cmd.Int("repeat"),
				Duration: cmd.Duration("duration"),
				Timeout:  cmd.Duration("timeout"),
			}
			fmt.Fprintf(os.Stderr, "Measuring %s at concurrency %v, %s each, %d sweep(s)\n", cfg.URL, levels, cfg.Duration, cfg.Repeats)
			ms, err := measure(ctx, cfg)
			if err != nil {
				return err
			}
			r, err := report.New(cfg.URL, ms)
			if err != nil {
				return err
			}

			// Keep doubling the largest level until a measured level is past
			// the fitted peak, so the peak is measured, not extrapolated.
			largest, maxC := levels[len(levels)-1], cmd.Int("max-concurrency")
			for r.Peak == nil || r.PeakExtrapolated {
				next := largest * 2
				if next > maxC {
					fmt.Fprintf(os.Stderr, "Stopping: concurrency %d would exceed --max-concurrency %d, so the peak isn't measured\n", next, maxC)
					break
				}
				if r.Peak == nil {
					fmt.Fprintf(os.Stderr, "No peak yet; measuring concurrency %d\n", next)
				} else {
					fmt.Fprintf(os.Stderr, "Predicted peak at %.1f is beyond %d; measuring concurrency %d\n", r.Peak.Concurrency, largest, next)
				}
				cfg.Levels = []int{next}
				more, err := measure(ctx, cfg)
				if err != nil {
					return err
				}
				ms = append(ms, more...)
				if r, err = report.New(cfg.URL, ms); err != nil {
					return err
				}
				largest = next
			}
			return writeReport(cmd, r)
		},
	}
}

// measure runs cfg, printing progress, and returns one measurement per step.
func measure(ctx context.Context, cfg loadtest.Config) ([]report.Measurement, error) {
	steps, err := loadtest.Run(ctx, cfg, func(s loadtest.Step) {
		fmt.Fprintf(os.Stderr, "  c=%-4d %10.1f req/s  %d errors\n", s.Concurrency, s.Throughput(), s.Errors)
	})
	if err != nil {
		return nil, err
	}
	ms := make([]report.Measurement, len(steps))
	for i, s := range steps {
		ms[i] = report.Measurement{
			Concurrency:   s.Concurrency,
			Throughput:    s.Throughput(),
			Requests:      s.Requests,
			Errors:        s.Errors,
			MeanLatencyMs: float64(s.MeanLatency.Microseconds()) / 1000,
		}
	}
	return ms, nil
}

func fitCommand() *cli.Command {
	return &cli.Command{
		Name:      "fit",
		Usage:     "estimate how a service scales from measurements in a CSV file (use - for stdin)",
		ArgsUsage: "<file.csv>",
		Description: "Use this for measurements fathom didn't take: any (N, throughput) pairs,\n" +
			"where N counts clients, workers, cores or replicas and throughput is successful\n" +
			"completions per second. Keep what N and a unit of work mean consistent.\n\n" +
			"The CSV needs a header row with concurrency and throughput columns. Other\n" +
			"columns are ignored, so `fathom run --format csv` output can be re-fitted.",
		Flags: outputFlags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return errors.New("expected one CSV file argument")
			}
			ms, err := readMeasurements(cmd.Args().First())
			if err != nil {
				return err
			}
			r, err := report.New("", ms)
			if err != nil {
				return err
			}
			return writeReport(cmd, r)
		},
	}
}

func outputFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "format",
			Aliases: []string{"f"},
			Usage:   "output format: " + strings.Join(report.Formats, ", "),
			Value:   "text",
			Validator: func(s string) error {
				if !slices.Contains(report.Formats, s) {
					return fmt.Errorf("unknown format %q (want one of %s)", s, strings.Join(report.Formats, ", "))
				}
				return nil
			},
		},
		&cli.StringFlag{
			Name:    "output",
			Aliases: []string{"o"},
			Usage:   "write the report to this file instead of stdout",
		},
	}
}

func writeReport(cmd *cli.Command, r report.Report) error {
	path := cmd.String("output")
	if path == "" {
		return report.Write(os.Stdout, cmd.String("format"), r)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := report.Write(f, cmd.String("format"), r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readMeasurements(path string) ([]report.Measurement, error) {
	var in io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		in = f
	}

	rows, err := csv.NewReader(in).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, errors.New("CSV has no data rows")
	}
	cCol, tCol := slices.Index(rows[0], "concurrency"), slices.Index(rows[0], "throughput")
	if cCol < 0 || tCol < 0 {
		return nil, errors.New("CSV header must include concurrency and throughput columns")
	}

	var ms []report.Measurement
	for i, row := range rows[1:] {
		c, err := strconv.Atoi(row[cCol])
		if err != nil {
			return nil, fmt.Errorf("row %d: concurrency: %w", i+2, err)
		}
		t, err := strconv.ParseFloat(row[tCol], 64)
		if err != nil {
			return nil, fmt.Errorf("row %d: throughput: %w", i+2, err)
		}
		ms = append(ms, report.Measurement{Concurrency: c, Throughput: t})
	}
	return ms, nil
}
