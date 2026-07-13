// Command gol regenerates the README banner (docs/assets/gol-cortex.svg):
// a real Game of Life simulation (B3/S23, dead boundary) seeded with the
// word CORTEX, evolved forward into soup, then played back to the exact
// original. The generations are baked into a self-looping SVG animated with
// pure CSS, so it renders on github.com without scripts. Output is
// deterministic — a function of the seed only.
//
// Usage: go run ./scripts/gol [-out docs/assets]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type grid struct {
	w, h  int
	cells []bool
}

func newGrid(w, h int) *grid { return &grid{w, h, make([]bool, w*h)} }

func (g *grid) at(x, y int) bool {
	if x < 0 || y < 0 || x >= g.w || y >= g.h {
		return false
	}
	return g.cells[y*g.w+x]
}

func (g *grid) set(x, y int) { g.cells[y*g.w+x] = true }

func (g *grid) step() *grid {
	n := newGrid(g.w, g.h)
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			live := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if (dx != 0 || dy != 0) && g.at(x+dx, y+dy) {
						live++
					}
				}
			}
			if g.at(x, y) {
				if live == 2 || live == 3 {
					n.set(x, y)
				}
			} else if live == 3 {
				n.set(x, y)
			}
		}
	}
	return n
}

type cell struct{ x, y int }

// crop returns the live cells inside the view rectangle, in view coordinates.
func (g *grid) crop(vx, vy, vw, vh int) []cell {
	var out []cell
	for y := 0; y < vh; y++ {
		for x := 0; x < vw; x++ {
			if g.at(vx+x, vy+y) {
				out = append(out, cell{x, y})
			}
		}
	}
	return out
}

func pathFor(cs []cell) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "M%d.05 %d.05h.9v.9h-.9z", c.x, c.y)
	}
	return b.String()
}

// renderSVG bakes the displayed frame sequence into a looping SVG. Frame i
// is shown by a negative animation-delay on a shared step keyframe. Cells
// that die linger as ghosts for three displayed frames at falling opacity;
// the trail is computed over the displayed order (cyclically), so the loop
// seam is invisible.
func renderSVG(frames [][]cell, vw, vh int, dt float64, width int, path string) error {
	n := len(frames)
	ghostOpacity := []string{".38", ".18", ".07"}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" role="img" aria-label="Conway's Game of Life">`, vw, vh, width)
	fmt.Fprintf(&b, `<style>.f{opacity:0;animation:k %.2fs step-end infinite}@keyframes k{0%%{opacity:1}%.4f%%{opacity:0}}@media (prefers-reduced-motion:reduce){.f{animation:none}.f:first-of-type{opacity:1}}.a{fill:#40c463}`,
		dt*float64(n), 100.0/float64(n))
	for i, op := range ghostOpacity {
		fmt.Fprintf(&b, ".g%d{fill:#40c463;fill-opacity:%s}", i+1, op)
	}
	b.WriteString(`</style>`)

	aliveAt := make([]map[cell]bool, n)
	for i, fr := range frames {
		m := make(map[cell]bool, len(fr))
		for _, c := range fr {
			m[c] = true
		}
		aliveAt[i] = m
	}

	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<g class="f" style="animation-delay:%.2fs">`, -(float64(n-i) * dt))
		fmt.Fprintf(&b, `<path class="a" d="%s"/>`, pathFor(frames[i]))
		for age := 1; age <= len(ghostOpacity); age++ {
			var ghosts []cell
			for c := range aliveAt[((i-age)%n+n)%n] {
				if aliveAt[i][c] {
					continue
				}
				newer := false
				for a := 1; a < age; a++ {
					if aliveAt[((i-a)%n+n)%n][c] {
						newer = true
						break
					}
				}
				if !newer {
					ghosts = append(ghosts, c)
				}
			}
			if len(ghosts) > 0 {
				sort.Slice(ghosts, func(a, b int) bool {
					if ghosts[a].y != ghosts[b].y {
						return ghosts[a].y < ghosts[b].y
					}
					return ghosts[a].x < ghosts[b].x
				})
				fmt.Fprintf(&b, `<path class="g%d" d="%s"/>`, age, pathFor(ghosts))
			}
		}
		b.WriteString(`</g>`)
	}
	b.WriteString(`</svg>`)
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

var font = map[rune][]string{
	'C': {"###", "#..", "#..", "#..", "###"},
	'O': {"###", "#.#", "#.#", "#.#", "###"},
	'R': {"##.", "#.#", "##.", "#.#", "#.#"},
	'T': {"###", ".#.", ".#.", ".#.", ".#."},
	'E': {"###", "#..", "##.", "#..", "###"},
	'X': {"#.#", "#.#", ".#.", "#.#", "#.#"},
}

func seedWord(g *grid, word string, x0, y0 int) {
	x := x0
	for _, r := range word {
		glyph := font[r]
		for row, s := range glyph {
			for col, ch := range s {
				if ch == '#' {
					g.set(x+col, y0+row)
				}
			}
		}
		x += len(glyph[0]) + 1
	}
}

func main() {
	out := flag.String("out", "docs/assets", "output directory")
	flag.Parse()

	// Sim grid is larger than the rendered view so growth never touches the
	// dead boundary within the captured window.
	const simW, simH = 91, 61
	const forward = 36
	const vx, vy, vw, vh = 20, 20, 51, 21

	g := newGrid(simW, simH)
	seedWord(g, "CORTEX", 34, 28)
	states := []*grid{g}
	for i := 0; i < forward; i++ {
		g = g.step()
		states = append(states, g)
	}

	// Boomerang: hold the seed, evolve forward, play back. Life is
	// irreversible; the point of the reverse pass is that recall isn't.
	var seq []int
	for i := 0; i < 6; i++ {
		seq = append(seq, 0)
	}
	for i := 1; i <= forward; i++ {
		seq = append(seq, i)
	}
	for i := forward - 1; i >= 1; i-- {
		seq = append(seq, i)
	}
	frames := make([][]cell, len(seq))
	for i, s := range seq {
		frames[i] = states[s].crop(vx, vy, vw, vh)
	}

	path := filepath.Join(*out, "gol-cortex.svg")
	if err := renderSVG(frames, vw, vh, 0.12, 760, path); err != nil {
		fmt.Fprintln(os.Stderr, "failed to write svg:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", path, "—", len(frames), "frames")
}
