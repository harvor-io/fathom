// Package loadtest collects (concurrency, throughput) measurements for the
// USL fit by driving HTTP requests at a series of fixed concurrency levels.
package loadtest

import (
	"context"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Config describes a stepped load test against a single URL.
type Config struct {
	URL      string
	Levels   []int         // concurrency levels to run, one step each
	Repeats  int           // how many times to sweep all levels
	Duration time.Duration // how long each step runs
	Timeout  time.Duration // per-request timeout
}

// Step is the outcome of running one concurrency level.
type Step struct {
	Concurrency int
	Requests    int // successful (status < 400) responses
	Errors      int // transport errors and status >= 400
	Elapsed     time.Duration
	MeanLatency time.Duration // over successful requests
}

// Throughput is successful requests per second.
func (s Step) Throughput() float64 {
	if s.Elapsed <= 0 {
		return 0
	}
	return float64(s.Requests) / s.Elapsed.Seconds()
}

// Run sweeps every level in turn, Repeats times, calling onStep after each
// step. Repeating whole sweeps rather than each level back to back keeps
// slow drift in the service from landing on a single level.
func Run(ctx context.Context, cfg Config, onStep func(Step)) ([]Step, error) {
	client := &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			// Keep one idle connection per worker so steps reuse
			// connections instead of exhausting ephemeral ports.
			MaxIdleConnsPerHost: slices.Max(cfg.Levels),
		},
	}

	var steps []Step
	for range max(cfg.Repeats, 1) {
		for _, n := range cfg.Levels {
			if err := ctx.Err(); err != nil {
				return steps, err
			}
			s := runStep(ctx, client, cfg, n)
			steps = append(steps, s)
			if onStep != nil {
				onStep(s)
			}
		}
	}
	return steps, nil
}

type workerStats struct {
	requests, errors int
	latency          time.Duration
}

func runStep(ctx context.Context, client *http.Client, cfg Config, n int) Step {
	stepCtx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()

	stats := make([]workerStats, n)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range n {
		wg.Go(func() {
			for stepCtx.Err() == nil {
				t := time.Now()
				ok := do(stepCtx, client, cfg.URL)
				if stepCtx.Err() != nil {
					return // cut off by end of step; don't count it
				}
				if ok {
					stats[i].requests++
					stats[i].latency += time.Since(t)
				} else {
					stats[i].errors++
				}
			}
		})
	}
	wg.Wait()

	s := Step{Concurrency: n, Elapsed: time.Since(start)}
	var latency time.Duration
	for _, w := range stats {
		s.Requests += w.requests
		s.Errors += w.errors
		latency += w.latency
	}
	if s.Requests > 0 {
		s.MeanLatency = latency / time.Duration(s.Requests)
	}
	return s
}

func do(ctx context.Context, client *http.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode < 400
}
