module github.com/dereksantos/cortex

go 1.26.0

// Pinned to the patch level that carries the stdlib security fixes
// govulncheck tracks; CI (test.yml, release.yml) pins the same version.
// Bump all three together.
toolchain go1.27.1

require (
	github.com/bwmarrin/discordgo v0.29.0
	github.com/charmbracelet/glamour v1.0.0
	github.com/mattn/go-runewidth v0.0.30
	github.com/viterin/vek v0.4.3
	golang.org/x/net v0.59.0
	golang.org/x/sys v0.48.0
	golang.org/x/term v0.46.0
)

require (
	github.com/alecthomas/chroma/v2 v2.27.0 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/lipgloss/v2 v2.0.6 // indirect
	github.com/charmbracelet/x/ansi v0.11.8 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/exp/golden v0.1.0 // indirect
	github.com/charmbracelet/x/exp/slice v0.1.0 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/chewxy/math32 v1.11.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/dlclark/regexp2/v2 v2.8.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/muesli/reflow v0.3.0 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/viterin/partial v1.1.0 // indirect
	github.com/xo/terminfo v1.2.0 // indirect
	github.com/yuin/goldmark v1.8.6 // indirect
	github.com/yuin/goldmark-emoji v1.0.6 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/text v0.42.0 // indirect
)
