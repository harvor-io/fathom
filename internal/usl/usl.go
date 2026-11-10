// Package usl fits measured throughput to the Universal Scalability Law:
//
//	X(N) = λN / (1 + σ(N-1) + κN(N-1))
//
// where N is concurrency, λ is single-worker throughput, σ is contention
// (serialization) and κ is coherency (crosstalk) cost.
package usl

import (
	"errors"
	"fmt"
	"math"
)

// Point is one measured throughput at a concurrency level.
type Point struct {
	Concurrency int
	Throughput  float64
}

// Model holds fitted USL coefficients.
type Model struct {
	Lambda float64 `json:"lambda"`
	Sigma  float64 `json:"sigma"`
	Kappa  float64 `json:"kappa"`
}

// Predict returns modelled throughput at concurrency n.
func (m Model) Predict(n float64) float64 {
	return m.Lambda * n / (1 + m.Sigma*(n-1) + m.Kappa*n*(n-1))
}

// Peak is the concurrency at which modelled throughput is highest.
type Peak struct {
	Concurrency float64 `json:"concurrency"`
	Throughput  float64 `json:"throughput"`
}

// Peak returns the throughput peak, or nil when the model has no peak
// (κ <= 0 means throughput never turns down; σ >= 1 means it never rises).
func (m Model) Peak() *Peak {
	if m.Kappa <= 0 || m.Sigma >= 1 {
		return nil
	}
	n := math.Sqrt((1 - m.Sigma) / m.Kappa)
	return &Peak{Concurrency: n, Throughput: m.Predict(n)}
}

// Fit fits a USL model to points using Gunther's linearisation: λ is taken
// from the measurement at N=1, then σ and κ come from a least-squares fit of
//
//	N/C(N) - 1 = (σ+κ)x + κx²,   x = N-1,  C(N) = X(N)/λ
//
// It returns the model and the R² of predicted versus measured throughput.
func Fit(points []Point) (Model, float64, error) {
	var lambda float64
	var ones int
	levels := map[int]bool{}
	for _, p := range points {
		if p.Concurrency < 1 {
			return Model{}, 0, fmt.Errorf("invalid concurrency %d", p.Concurrency)
		}
		if p.Throughput <= 0 {
			return Model{}, 0, fmt.Errorf("concurrency %d has no throughput; cannot fit", p.Concurrency)
		}
		levels[p.Concurrency] = true
		if p.Concurrency == 1 {
			lambda += p.Throughput
			ones++
		}
	}
	if ones == 0 {
		return Model{}, 0, errors.New("measurements must include concurrency 1")
	}
	if len(levels) < 3 {
		return Model{}, 0, errors.New("need at least 3 distinct concurrency levels to fit")
	}
	lambda /= float64(ones)

	var sxx, sx3, sx4, sxy, sx2y float64
	for _, p := range points {
		n := float64(p.Concurrency)
		x := n - 1
		y := n/(p.Throughput/lambda) - 1
		sxx += x * x
		sx3 += x * x * x
		sx4 += x * x * x * x
		sxy += x * y
		sx2y += x * x * y
	}
	det := sxx*sx4 - sx3*sx3
	if det == 0 {
		return Model{}, 0, errors.New("degenerate measurements; cannot fit")
	}
	a := (sxy*sx4 - sx3*sx2y) / det
	b := (sxx*sx2y - sx3*sxy) / det
	m := Model{Lambda: lambda, Sigma: a - b, Kappa: b}

	return m, rSquared(m, points), nil
}

func rSquared(m Model, points []Point) float64 {
	var mean float64
	for _, p := range points {
		mean += p.Throughput
	}
	mean /= float64(len(points))
	var ssRes, ssTot float64
	for _, p := range points {
		d := p.Throughput - m.Predict(float64(p.Concurrency))
		ssRes += d * d
		t := p.Throughput - mean
		ssTot += t * t
	}
	if ssTot == 0 {
		return 1
	}
	return 1 - ssRes/ssTot
}
