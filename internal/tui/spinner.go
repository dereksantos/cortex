package tui

// Spinner manages animation frame sequences.
// It tracks the current frame and provides methods to advance through the sequence.
type Spinner struct {
	frames []string
	index  int
}

// NewSpinner creates a new spinner with the given animation frames.
func NewSpinner(frames []string) *Spinner {
	if len(frames) == 0 {
		frames = []string{" "} // fallback to prevent panic
	}
	return &Spinner{
		frames: frames,
		index:  0,
	}
}

// Current returns the current frame without advancing.
func (s *Spinner) Current() string {
	return s.frames[s.index]
}

// Next advances to the next frame and returns it.
// Wraps around to the beginning when reaching the end.
func (s *Spinner) Next() string {
	s.index = (s.index + 1) % len(s.frames)
	return s.frames[s.index]
}

// Frame returns the frame at the specified index (with wrapping).
// Does not change the spinner's current position.
func (s *Spinner) Frame(index int) string {
	if len(s.frames) == 0 {
		return " "
	}
	// Handle negative indices and wrapping
	idx := index % len(s.frames)
	if idx < 0 {
		idx += len(s.frames)
	}
	return s.frames[idx]
}

// Reset returns the spinner to the first frame.
func (s *Spinner) Reset() {
	s.index = 0
}

// Len returns the number of frames in the spinner.
func (s *Spinner) Len() int {
	return len(s.frames)
}

// Frames returns a copy of all frames.
func (s *Spinner) Frames() []string {
	result := make([]string, len(s.frames))
	copy(result, s.frames)
	return result
}

// Predefined spinner frame sequences for cognitive modes.
// These match the animations used in the Cortex TUI.
var (
	// SpinnerDream - dreamy, flowing animation for Dream mode
	SpinnerDream = []string{"~", "~~", "~~~", "~~~~", "~~~~~", "~~~~", "~~~", "~~"}

	// SpinnerThink - contemplative dots for Think mode
	SpinnerThink = []string{".", "..", "...", "....", "...", ".."}

	// SpinnerReflect - pulsing brackets for Reflect mode
	SpinnerReflect = []string{"[    ]", "[ .  ]", "[ .. ]", "[ ...]", "[... ]", "[..  ]", "[.   ]"}

	// SpinnerReflex - quick dash for fast Reflex mode
	SpinnerReflex = []string{"-", "=", "-"}

	// SpinnerResolve - decision-making arrows
	SpinnerResolve = []string{">", ">>", ">>>", ">>", ">"}

	// SpinnerInsight - lightbulb/star effect for insights
	SpinnerInsight = []string{"*", "+", "*", "."}

	// SpinnerDigest - processing indicator for digest operations
	SpinnerDigest = []string{"[=   ]", "[==  ]", "[=== ]", "[====]", "[ ===]", "[  ==]", "[   =]"}

	// SpinnerClassic - classic spinner for general use
	SpinnerClassic = []string{"|", "/", "-", "\\"}

	// SpinnerDots - simple dots animation
	SpinnerDots = []string{"   ", ".  ", ".. ", "..."}

	// SpinnerBraille - smooth braille spinner
	SpinnerBraille = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

	// SpinnerBlocks - block building animation
	SpinnerBlocks = []string{"▏", "▎", "▍", "▌", "▋", "▊", "▉", "█", "▉", "▊", "▋", "▌", "▍", "▎"}
)

// ModeSpinners maps cognitive mode names to their spinner frames.
var ModeSpinners = map[string][]string{
	"dream":   SpinnerDream,
	"think":   SpinnerThink,
	"reflect": SpinnerReflect,
	"reflex":  SpinnerReflex,
	"resolve": SpinnerResolve,
	"insight": SpinnerInsight,
	"digest":  SpinnerDigest,
}

// GetModeSpinner returns the spinner frames for a cognitive mode.
// Returns SpinnerClassic if the mode is not found.
func GetModeSpinner(mode string) []string {
	if frames, ok := ModeSpinners[mode]; ok {
		return frames
	}
	return SpinnerClassic
}

// GetModeFrame returns a specific frame for a cognitive mode's spinner.
// The frame index wraps around if out of bounds.
func GetModeFrame(mode string, frame int) string {
	frames := GetModeSpinner(mode)
	if len(frames) == 0 {
		return " "
	}
	idx := frame % len(frames)
	if idx < 0 {
		idx += len(frames)
	}
	return frames[idx]
}

// NewModeSpinner creates a spinner for the specified cognitive mode.
func NewModeSpinner(mode string) *Spinner {
	return NewSpinner(GetModeSpinner(mode))
}
