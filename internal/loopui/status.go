package loopui

import (
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/internal/style"
)

// HumanCost formats a dollar cost with precision that scales to the magnitude.
func HumanCost(c float64) string {
	switch {
	case c >= 1:
		return fmt.Sprintf("$%.2f", c)
	case c >= 0.01:
		return fmt.Sprintf("$%.3f", c)
	default:
		return fmt.Sprintf("$%.4f", c)
	}
}

// HumanK renders a token count compactly: 8200 -> "8.2k", 999 -> "999".
func HumanK(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n >= 1_000_000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "M"
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
}

// ContextColor shifts a context gauge green -> yellow -> red as the window
// fills. Red starts at redThreshold.
func ContextColor(used, max int, redThreshold float64) style.Role {
	switch r := float64(used) / float64(max); {
	case r < 0.5:
		return style.OK
	case r < redThreshold:
		return style.Warn
	default:
		return style.Err
	}
}
