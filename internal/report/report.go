// Package report turns a USL fit into a scaling estimate and renders it in
// the supported output formats.
package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"text/tabwriter"

	"github.com/harvor-io/fathom/internal/usl"
)

// Formats lists the supported output formats.
var Formats = []string{"text", "json", "csv"}

// Report is fathom's estimate of how one service scales.
type Report struct {
	Target string    `json:"target,omitempty"`
	Model  usl.Model `json:"model"`
	R2     float64   `json:"r2"`
	Peak   *usl.Peak `json:"peak"` // nil when the model has no peak
	// PeakExtrapolated is true when the peak lies beyond the largest
	// measured concurrency, so the data doesn't confirm it.
	PeakExtrapolated bool `json:"peak_extrapolated"`
	// Levels summarises each measured concurrency, followed by projected
	// levels beyond the measured range.
	Levels       []Level       `json:"levels"`
	Measurements []Measurement `json:"measurements"`
}

// Measurement is one throughput measurement. Requests, Errors and
// MeanLatencyMs are only known for live runs; they are context for judging
// whether the throughput is usable, not inputs to the fit.
type Measurement struct {
	Concurrency   int     `json:"concurrency"`
	Throughput    float64 `json:"throughput"`
	Requests      int     `json:"requests,omitempty"`
	Errors        int     `json:"errors,omitempty"`
	MeanLatencyMs float64 `json:"mean_latency_ms,omitempty"`
}

// Level is the estimate at one concurrency. Runs is 0 for projected levels.
type Level struct {
	Concurrency   int     `json:"concurrency"`
	Runs          int     `json:"runs"`
	Throughput    float64 `json:"throughput,omitempty"` // mean of runs
	Min           float64 `json:"min,omitempty"`
	Max           float64 `json:"max,omitempty"`
	Predicted     float64 `json:"predicted"`
	Efficiency    float64 `json:"efficiency"` // predicted / (λN): share of linear scaling
	Errors        int     `json:"errors,omitempty"`
	MeanLatencyMs float64 `json:"mean_latency_ms,omitempty"`
}

// New fits ms and builds the report.
func New(target string, ms []Measurement) (Report, error) {
	points := make([]usl.Point, len(ms))
	for i, m := range ms {
		points[i] = usl.Point{Concurrency: m.Concurrency, Throughput: m.Throughput}
	}
	model, r2, err := usl.Fit(points)
	if err != nil {
		return Report{}, err
	}

	r := Report{Target: target, Model: model, R2: r2, Peak: model.Peak(), Measurements: ms}

	byLevel := map[int][]Measurement{}
	for _, m := range ms {
		byLevel[m.Concurrency] = append(byLevel[m.Concurrency], m)
	}
	levels := make([]int, 0, len(byLevel))
	for n := range byLevel {
		levels = append(levels, n)
	}
	slices.Sort(levels)

	for _, n := range levels {
		l := r.level(n)
		runs := byLevel[n]
		l.Runs = len(runs)
		l.Min, l.Max = runs[0].Throughput, runs[0].Throughput
		var latency float64
		for _, m := range runs {
			l.Throughput += m.Throughput
			l.Min = min(l.Min, m.Throughput)
			l.Max = max(l.Max, m.Throughput)
			l.Errors += m.Errors
			latency += m.MeanLatencyMs
		}
		l.Throughput /= float64(len(runs))
		l.MeanLatencyMs = latency / float64(len(runs))
		r.Levels = append(r.Levels, l)
	}

	largest := levels[len(levels)-1]
	for _, n := range []int{largest * 2, largest * 4} {
		r.Levels = append(r.Levels, r.level(n))
	}
	r.PeakExtrapolated = r.Peak != nil && r.Peak.Concurrency > float64(largest)
	return r, nil
}

func (r Report) level(n int) Level {
	pred := r.Model.Predict(float64(n))
	return Level{Concurrency: n, Predicted: pred, Efficiency: pred / (r.Model.Lambda * float64(n))}
}

// Write renders r to w in the given format.
func Write(w io.Writer, format string, r Report) error {
	switch format {
	case "text":
		return writeText(w, r)
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "csv":
		return writeCSV(w, r)
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

func writeText(w io.Writer, r Report) error {
	if r.Target != "" {
		fmt.Fprintf(w, "Target:  %s\n", r.Target)
	}
	fmt.Fprintf(w, "Model:   λ=%.4g  σ=%.4g  κ=%.4g  (R²=%.4f over %d measurements)\n",
		r.Model.Lambda, r.Model.Sigma, r.Model.Kappa, r.R2, len(r.Measurements))
	switch {
	case r.Peak == nil:
		fmt.Fprintln(w, "Peak:    none; the model never turns down (κ <= 0 or σ >= 1)")
	case r.PeakExtrapolated:
		fmt.Fprintf(w, "Peak:    %.1f/s at concurrency %.1f (extrapolated, not measured)\n", r.Peak.Throughput, r.Peak.Concurrency)
	default:
		fmt.Fprintf(w, "Peak:    %.1f/s at concurrency %.1f\n", r.Peak.Throughput, r.Peak.Concurrency)
	}
	fmt.Fprintln(w)

	// Errors and latency only exist for live runs, which have a target.
	live := r.Target != ""
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprint(tw, "concurrency\tmeasured\tmin–max\tpredicted\tefficiency\t")
	if live {
		fmt.Fprint(tw, "errors\tlatency ms\t")
	}
	fmt.Fprintln(tw)
	for _, l := range r.Levels {
		if l.Runs == 0 {
			fmt.Fprintf(tw, "%d\tprojected\t\t%.1f\t%.0f%%\t", l.Concurrency, l.Predicted, l.Efficiency*100)
			if live {
				fmt.Fprint(tw, "\t\t")
			}
			fmt.Fprintln(tw)
			continue
		}
		spread := "" // a single run has no spread
		if l.Runs > 1 {
			spread = fmt.Sprintf("%.1f–%.1f", l.Min, l.Max)
		}
		fmt.Fprintf(tw, "%d\t%.1f\t%s\t%.1f\t%.0f%%\t", l.Concurrency, l.Throughput, spread, l.Predicted, l.Efficiency*100)
		if live {
			fmt.Fprintf(tw, "%d\t%.2f\t", l.Errors, l.MeanLatencyMs)
		}
		fmt.Fprintln(tw)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	var notes []string
	if r.PeakExtrapolated {
		notes = append(notes, "The peak is beyond the largest measured level, so it's uncertain. Measure higher concurrency to confirm it.")
	}
	if slices.ContainsFunc(r.Levels, func(l Level) bool { return l.Runs == 1 }) {
		notes = append(notes, "Some levels were measured once, so their noise is unknown. Repeat levels to see it.")
	}
	if len(notes) > 0 {
		fmt.Fprintln(w)
		for _, n := range notes {
			fmt.Fprintln(w, "Note:", n)
		}
	}
	return nil
}

// writeCSV writes one row per measurement. Its concurrency and throughput
// columns are what `fathom fit` reads, so a run's CSV can be re-fitted later.
func writeCSV(w io.Writer, r Report) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"concurrency", "throughput", "predicted", "requests", "errors", "mean_latency_ms"})
	for _, m := range r.Measurements {
		_ = cw.Write([]string{
			strconv.Itoa(m.Concurrency),
			ftoa(m.Throughput),
			ftoa(r.Model.Predict(float64(m.Concurrency))),
			strconv.Itoa(m.Requests),
			strconv.Itoa(m.Errors),
			ftoa(m.MeanLatencyMs),
		})
	}
	cw.Flush()
	return cw.Error()
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
