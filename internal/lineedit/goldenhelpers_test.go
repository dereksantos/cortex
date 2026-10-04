package lineedit

// goldenhelpers_test.go — shared test helpers for the golden snapshots: the
// fixed prompt and the case-name.

// prompt is the fixed prompt every anchored frame renders with.
const prompt = "> "

// caseName names a width axis corner: the width (w0 for the non-TTY no-width
// path, which renderLine falls back to 80). The anchored frame has no
// color axis (lineedit's SGR constants are hardcoded), so width is the only
// axis here.
func caseName(w int) string {
	if w == 0 {
		return "w0"
	}
	return "w" + itoa(w)
}

// itoa is a tiny int→string for caseName (avoids importing strconv in a
// helper file that's otherwise stdlib-light).
func itoa(w int) string {
	if w == 0 {
		return "0"
	}
	digits := make([]byte, 0, 3)
	for w > 0 {
		digits = append([]byte{byte('0' + w%10)}, digits...)
		w /= 10
	}
	return string(digits)
}
