package eval

import (
	"math"
	"sort"
)

// PearsonCorrelation computes the Pearson product-moment correlation coefficient
// between two equal-length slices. Returns 0 if fewer than 2 points or zero variance.
func PearsonCorrelation(x, y []float64) float64 {
	n := len(x)
	if n != len(y) || n < 2 {
		return 0
	}

	// Means
	var sumX, sumY float64
	for i := 0; i < n; i++ {
		sumX += x[i]
		sumY += y[i]
	}
	meanX := sumX / float64(n)
	meanY := sumY / float64(n)

	// Covariance and standard deviations
	var cov, varX, varY float64
	for i := 0; i < n; i++ {
		dx := x[i] - meanX
		dy := y[i] - meanY
		cov += dx * dy
		varX += dx * dx
		varY += dy * dy
	}

	denom := math.Sqrt(varX * varY)
	if denom == 0 {
		return 0
	}
	return cov / denom
}

// SpearmanCorrelation computes the Spearman rank correlation coefficient.
// This is the Pearson correlation of the rank values.
func SpearmanCorrelation(x, y []float64) float64 {
	n := len(x)
	if n != len(y) || n < 2 {
		return 0
	}

	rankX := ranks(x)
	rankY := ranks(y)
	return PearsonCorrelation(rankX, rankY)
}

// ranks converts values to their ranks (1-based, average rank for ties).
func ranks(vals []float64) []float64 {
	n := len(vals)
	type indexed struct {
		val float64
		idx int
	}

	sorted := make([]indexed, n)
	for i, v := range vals {
		sorted[i] = indexed{val: v, idx: i}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].val < sorted[j].val
	})

	result := make([]float64, n)
	i := 0
	for i < n {
		// Find run of equal values
		j := i + 1
		for j < n && sorted[j].val == sorted[i].val {
			j++
		}
		// Average rank for ties
		avgRank := float64(i+j+1) / 2.0
		for k := i; k < j; k++ {
			result[sorted[k].idx] = avgRank
		}
		i = j
	}
	return result
}
