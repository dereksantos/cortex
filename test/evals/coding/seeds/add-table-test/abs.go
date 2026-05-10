// Package mathx provides small numeric helpers.
package mathx

// Abs returns the absolute value of x.
func Abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
