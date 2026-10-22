package tui

import (
	"image"
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

// The look: Barad's own two themes, night (Night terrain) and paper, drawn
// edge to edge. The app sets the terminal's background and foreground to the
// theme's canvas and ink, so every cell without a colour is canvas. A surface
// is a plain rectangle; a row of ▄ above and ▀ below gives it half a row of
// padding, and ▐ ▌ give a one-line pill half a cell at each end.

// slab lays lines on a surface exactly w cells wide, with padX cells inside
// on each side and half a row of padding above and below. Lines are cut to
// fit; each row comes back painted, ready to sit beside other rows.
func slab(lines []Line, bg color.Color, w, padX int) []Line {
	edge := Fg(bg)
	out := []Line{{S(strings.Repeat("▄", w), edge)}}
	pad := S(strings.Repeat(" ", padX), lipgloss.NewStyle().Background(bg))
	for _, l := range lines {
		row := append(Line{pad}, on(l, bg)...)
		out = append(out, Line{S(row.Paint(bg, w), lipgloss.NewStyle())})
	}
	return append(out, Line{S(strings.Repeat("▀", w), edge)})
}

// block is a slab x cells from the left edge, painted.
func block(lines []Line, bg color.Color, x, w, padX int) []string {
	var out []string
	for _, l := range slab(lines, bg, w, padX) {
		out = append(out, append(Line{gap(x)}, l...).Paint(nil, 0))
	}
	return out
}

// pill is a one-line label on a surface.
func pill(l Line, bg color.Color) Line {
	out := Line{S("▐", Fg(bg))}
	out = append(out, on(append(Line{S(" ", SText)}, append(l, S(" ", SText))...), bg)...)
	return append(out, S("▌", Fg(bg)))
}

// ---------------------------------------------------------------- masthead

// The masthead is the Barad landscape, drawn in half-block pixels across the
// whole width: three ridges, a river of light, the shore and the tower. Night
// is the same land after sunset, with the sun half behind the far ridge and a
// few stars. The tower's light is always on.

type pt struct{ x, y float64 }

// The landscape's outlines, in the 1440×900 coordinates of the web art.
var (
	ridgeFar  = []pt{{0, 476}, {188, 342}, {343, 447}, {528, 273}, {730, 470}, {930, 342}, {1100, 497}, {1268, 398}, {1440, 512}}
	ridgeMid  = []pt{{0, 580}, {218, 450}, {406, 565}, {631, 406}, {818, 557}, {996, 447}, {1160, 578}, {1308, 499}, {1440, 585}}
	ridgeNear = []pt{{0, 674}, {185, 566}, {378, 657}, {588, 549}, {804, 643}, {980, 571}, {1160, 662}, {1310, 603}, {1440, 676}}
	river     = []pt{{0, 708}, {250, 684}, {500, 712}, {720, 735}, {1000, 700}, {1200, 684}, {1440, 714}}
	shore     = []pt{{0, 760}, {250, 724}, {450, 745}, {640, 769}, {900, 735}, {1100, 715}, {1440, 778}}
	ground    = []pt{{0, 820}, {250, 802}, {500, 824}, {760, 850}, {1050, 822}, {1250, 806}, {1440, 828}}
)

// along reads an outline's height at x: straight between peaks, eased between
// the points of a wave.
func along(ps []pt, x float64, smooth bool) float64 {
	for i := 1; i < len(ps); i++ {
		if x <= ps[i].x {
			t := (x - ps[i-1].x) / (ps[i].x - ps[i-1].x)
			if smooth {
				t = (1 - math.Cos(t*math.Pi)) / 2
			}
			return ps[i-1].y + t*(ps[i].y-ps[i-1].y)
		}
	}
	return ps[len(ps)-1].y
}

type land struct {
	skyTop, skyLow, sun, far, mid, near, river, shore, ground, tower, star, starBright color.RGBA
	quiet, dim                                                                         color.RGBA // text on the ground, and the gauge's empty track
	sunX, sunY, sunR, glow                                                             float64
	stars                                                                              bool
}

func hex(s string) color.RGBA {
	var v uint32
	for _, c := range s[1:] {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint32(c - '0')
		default:
			v |= uint32(c-'a') + 10
		}
	}
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	t = math.Max(0, math.Min(1, t))
	f := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return color.RGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), 255}
}

var lands = map[string]land{
	"night": {
		skyTop: hex("#090d12"), skyLow: hex("#1c2430"), sun: hex("#ff5b35"),
		far: hex("#27333f"), mid: hex("#1d2732"), near: hex("#161e27"),
		river: mix(hex("#161e27"), hex("#ff5b35"), 0.55), shore: hex("#10161d"), ground: hex("#0b1015"),
		tower: hex("#06090c"), star: hex("#4a5562"), starBright: hex("#cac7c0"),
		quiet: hex("#8b95a0"), dim: hex("#273240"),
		sunX: 1180, sunY: 482, sunR: 70, glow: 0.42, stars: true,
	},
	"paper": {
		skyTop: hex("#f1efe6"), skyLow: hex("#f4eee2"), sun: hex("#ff5c35"),
		far: hex("#a8b4c0"), mid: hex("#71879a"), near: hex("#526b84"),
		river: hex("#f1efe6"), shore: hex("#2e4a63"), ground: hex("#18273a"),
		tower: hex("#18273a"), quiet: hex("#a8b4c0"), dim: hex("#2e4a63"),
		sunX: 1165, sunY: 300, sunR: 64, glow: 0.10,
	},
}

func hash(x, y int) float64 {
	h := uint32(x*374761393 + y*668265263)
	h = (h ^ (h >> 13)) * 1274126177
	return float64(h^(h>>16)) / float64(1<<32)
}

// pixel is the landscape's colour at (x, y) in art coordinates. Past the
// art's right edge the land mirrors itself, so a wide banner keeps its peaks
// steep instead of stretching them; the sun and tower stay where they are.
func (l land) pixel(x, y float64, px, py int, step, aspect float64) color.RGBA {
	lx := math.Mod(x, 2880)
	if lx > 1440 {
		lx = 2880 - lx
	}
	c := mix(l.skyTop, l.skyLow, (y-200)/400)
	if l.stars && step < 8 && y < along(ridgeFar, lx, false)-20 { // stars only where pixels are fine
		if h := hash(px, py); h < 0.0016 {
			c = mix(c, l.star, 0.9)
			if h < 0.0004 {
				c = l.starBright
			}
		}
	}
	d := math.Hypot((x-l.sunX)*aspect, y-l.sunY) // round on screen, however the land is squashed
	switch {
	case d <= l.sunR:
		c = l.sun
	case d < l.sunR*4:
		f := 1 - (d-l.sunR)/(l.sunR*3)
		c = mix(c, l.sun, l.glow*f*f)
	}
	if y >= along(ridgeFar, lx, false) {
		c = l.far
	}
	if y >= along(ridgeMid, lx, false) {
		c = l.mid
	}
	if y >= along(ridgeNear, lx, false) {
		c = l.near
	}
	if ry := along(river, lx, true); math.Abs(y-ry) <= 3 { // a hairline; coarse pixels average it to a glow
		c = l.river
	}
	if y >= along(shore, lx, true) {
		c = l.shore
	}
	if y >= along(ground, lx, true) {
		c = l.ground
	}
	// the tower: a mast with two crossbars under an arrowhead, its light lit
	tx, base, top := 136.0, 700.0, 372.0
	bar := 6.0
	switch {
	case y >= top && y <= top+36 && math.Abs(x-tx) <= 18*(y-top)/36+1:
		c = l.tower
	case y > top+36 && y <= base && math.Abs(x-tx) <= 8:
		c = l.tower
	case math.Abs(y-(top+90)) <= bar && math.Abs(x-tx) <= 16, math.Abs(y-(top+135)) <= bar && math.Abs(x-tx) <= 13:
		c = l.tower
	}
	if math.Abs(y-(top+56)) <= 5 && math.Abs(x-tx) <= 5 {
		c = l.sun
	}
	return c
}

// landscape draws the masthead as a picture for cols×rows cells, each pixel
// the mean of a grid of samples. For kitty a cell is 10×20 pixels; for half blocks
// it is 1×2, so the picture is drawn at exactly the size it is shown.
func landscape(theme string, cols, rows int, g Graphics) *Picture {
	l := lands[theme]
	W, H, n := cols, rows*2, 8 // coarse pixels take more samples, so hairlines average out evenly
	if g == GraphicsKitty {
		W, H, n = cols*10, rows*20, 2
	}
	// the art's height from yTop down to 860, and as much width as keeps the
	// land squashed no more than 2:1
	const yTop, yBot = 230.0, 880.0
	ys := (yBot - yTop) / float64(H)
	xs := math.Max(1440/float64(W), ys/2)
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	for py := 0; py < H; py++ {
		for px := 0; px < W; px++ {
			var r, g, b float64
			for i := 0; i < n*n; i++ {
				ox, oy := (float64(i%n)+0.5)/float64(n), (float64(i/n)+0.5)/float64(n)
				c := l.pixel((float64(px)+ox)*xs, yTop+(float64(py)+oy)*ys, px, py, ys, ys/xs)
				r, g, b = r+float64(c.R), g+float64(c.G), b+float64(c.B)
			}
			k := float64(n * n)
			img.SetRGBA(px, py, color.RGBA{uint8(r / k), uint8(g / k), uint8(b / k), 255})
		}
	}
	return DrawnPicture("cover", img)
}

// masthead is the cover: the landscape, then a row of shore carrying the
// wordmark on the left and right on the right.
func masthead(p *Picture, theme string, w int, g Graphics, right Line) []string {
	var out []string
	for _, l := range p.Lines(w, g) {
		out = append(out, l.Paint(nil, 0))
	}
	word := Line{gap(margin), S("Barad", lipgloss.NewStyle().Foreground(hex("#f1efe6")).Bold(true))}
	row := spread(word, append(right, gap(margin)), w)
	return append(out, row.Paint(lands[theme].ground, w))
}

// ---------------------------------------------------------------- the ground

// The ground is the cover's shore carried onto every screen: the bar across
// the top of a session, and the title strip of every sheet. It is dark in
// both themes, so its ink is the same: bone text and the night's signal hues.
var (
	groundText   = hex("#f1efe6")
	groundSec    = hex("#cac7c0")
	groundAgent  = hex("#88bfe6")
	groundOk     = hex("#8bcca1")
	groundDanger = hex("#f28eab")
	groundAccent = hex("#fd9a73")
)

// bar is the top of a screen: a full-width strip of ground with where you
// are (crumbs, then the title) and a status on the right, and a line of
// detail under it. A half-block edge eases it into the page; while an agent
// works (sweep ≥ 0) a band of blue light runs along that edge.
func bar(theme string, w int, crumbs []string, title string, right, detail Line, sweep int) []string {
	l := lands[theme]
	var head Line
	head = append(head, gap(margin))
	for _, c := range crumbs {
		head = append(head, S(strings.ToUpper(c), Fg(l.quiet).Bold(true)), S("  /  ", Fg(l.quiet)))
	}
	head = append(head, S(title, Fg(groundText).Bold(true)))
	head = spread(head, append(right, gap(margin)), w)
	edge := Fg(l.ground).Render(strings.Repeat("▀", w))
	if sweep >= 0 {
		const run = 24
		head := sweep*2%(w+run) - run
		var b strings.Builder
		for c := 0; c < w; c++ {
			col := l.ground
			if d := head + run - c; d >= 0 && d < run {
				col = mix(l.ground, groundAgent, 1-float64(d)/run) // brightest at the head
			}
			b.WriteString(Fg(col).Render("▀"))
		}
		edge = b.String()
	}
	return []string{
		Line{}.Paint(l.ground, w),
		head.Paint(l.ground, w),
		append(Line{gap(margin)}, detail...).Paint(l.ground, w),
		Line{}.Paint(l.ground, w),
		edge,
	}
}

// gauge is a share drawn as a short rule: filled in the ground's text,
// the rest in its dim track.
func gauge(theme string, frac float64, cells int, fill color.Color) Line {
	n := min(cells, max(0, int(frac*float64(cells)+0.5)))
	return Line{S(strings.Repeat("━", n), Fg(fill)), S(strings.Repeat("━", cells-n), Fg(lands[theme].dim))}
}

// sheet is a pop-over: a strip of ground carrying the title, then a raised
// body. tone tints the strip (a danger strip for a question that destroys).
func sheet(theme string, title, right Line, body []Line, w int, tone color.Color) string {
	if tone == nil {
		tone = lands[theme].ground
	}
	out := []string{
		Fg(tone).Render(strings.Repeat("▄", w)),
		append(Line{gap(4)}, spread(title, right, w-8)...).Paint(tone, w),
		Fg(Raised).Background(tone).Render(strings.Repeat("▄", w)),
	}
	pad := lipgloss.NewStyle().Background(Raised)
	for _, l := range append(append([]Line{{}}, body...), Line{}) {
		row := append(Line{S("    ", pad)}, on(l, Raised)...)
		out = append(out, row.Paint(Raised, w))
	}
	out = append(out, Fg(Raised).Render(strings.Repeat("▀", w)))
	return strings.Join(out, "\n")
}

// button is a key and what it does, on a solid fill.
func button(key, label string, fg, bg color.Color) Line {
	return pill(Line{S(key, lipgloss.NewStyle().Foreground(fg).Bold(true)), S("  "+label, Fg(fg))}, bg)
}
