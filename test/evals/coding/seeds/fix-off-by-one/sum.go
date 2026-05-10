// Package obo exposes a SumRange helper that has an off-by-one bug.
package obo

// SumRange returns the sum of all integers in [lo, hi] INCLUSIVE.
// (lo and hi both contribute to the total.)
//
// BUG: the current implementation is exclusive on the upper bound.
// Tests in sum_test.go fail until the bug is fixed.
func SumRange(lo, hi int) int {
	total := 0
	for i := lo; i < hi; i++ {
		total += i
	}
	return total
}
