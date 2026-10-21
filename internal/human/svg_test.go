package human

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// go test ./internal/human -run TestWriteFigures -figures docs/img
// regenerates the README figures from the real engine on the simulated screen.
var figuresDir = flag.String("figures", "", "write README figures (SVG) to this directory")

type sample struct {
	t    time.Duration
	x, y float64
}

// trace runs one move and records where the cursor is after every report.
func trace(seed uint64, from, to Point) []sample {
	e, rec, scr := newSim(seed, from)
	out := []sample{{0, from.X, from.Y}}
	rec.OnPointer = func(dx, dy int) {
		scr.apply(dx, dy)
		out = append(out, sample{scr.elapsed, scr.pending.X, scr.pending.Y})
	}
	if _, err := e.MoveTo(context.Background(), to, 20); err != nil {
		panic(err)
	}
	return out
}

var palette = []string{"#2563eb", "#dc2626", "#059669", "#d97706", "#7c3aed", "#db2777"}

const svgStyle = `<style>text{font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;fill:#334155}.t{font-size:15px;font-weight:600}.s{font-size:12px;fill:#64748b}</style>`

func TestWriteFigures(t *testing.T) {
	if *figuresDir == "" {
		t.Skip("pass -figures DIR to write figures")
	}
	if err := os.MkdirAll(*figuresDir, 0o755); err != nil {
		t.Fatal(err)
	}
	from, to := Point{150, 820}, Point{1300, 260}
	var runs [][]sample
	for i := range 4 {
		runs = append(runs, trace(uint64(11+i*7), from, to))
	}
	write(t, "trajectories.svg", trajectories(runs, from, to))
	write(t, "speed-profile.svg", speedProfile(runs[:3]))
	write(t, "cursor.svg", animated(runs[:3], from, to))
}

func write(t *testing.T, name, svg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(*figuresDir, name), []byte(svg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// trajectories draws several moves between the same two points: every path
// is different, and dots (one per 8 ms report) are spaced by speed.
func trajectories(runs [][]sample, from, to Point) string {
	const w, h, pad, head = 880.0, 520.0, 50.0, 70.0
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, run := range runs {
		for _, s := range run {
			minX, maxX = math.Min(minX, s.x), math.Max(maxX, s.x)
			minY, maxY = math.Min(minY, s.y), math.Max(maxY, s.y)
		}
	}
	k := math.Min((w-2*pad)/(maxX-minX), (h-head-2*pad)/(maxY-minY))
	px := func(x float64) float64 { return pad + (x-minX)*k }
	py := func(y float64) float64 { return head + pad + (y-minY)*k }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f">%s`, w, h, w, h, svgStyle)
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" rx="12" fill="#ffffff" stroke="#e2e8f0"/>`)
	fmt.Fprintf(&b, `<text x="24" y="34" class="t">Same start, same target, %d different paths</text>`, len(runs))
	fmt.Fprintf(&b, `<text x="24" y="54" class="s">One dot every 8 ms: dense where the hand is slow (start, end), sparse where it is fast (middle).</text>`)
	for i, run := range runs {
		c := palette[i%len(palette)]
		var pts []string
		for _, s := range run {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", px(s.x), py(s.y)))
		}
		fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2" stroke-opacity="0.8"/>`, strings.Join(pts, " "), c)
		for _, s := range run {
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="2.2" fill="%s"/>`, px(s.x), py(s.y), c)
		}
	}
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="7" fill="#0f172a"/>`, px(from.X), py(from.Y))
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="s">start</text>`, px(from.X)+12, py(from.Y)+18)
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="#0f172a" stroke-width="2"/>`, px(to.X), py(to.Y), math.Max(10*k, 6))
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="s" text-anchor="end">target</text>`, px(to.X)-14, py(to.Y)+24)
	b.WriteString(`</svg>`)
	return b.String()
}

// speedProfile plots cursor speed over time: a bell-shaped main stroke, then a
// small bump for the correction after an overshoot.
func speedProfile(runs [][]sample) string {
	const w, h = 900.0, 360.0
	const left, right, top, bottom = 70.0, 30.0, 70.0, 50.0
	var maxT, maxV float64
	speeds := make([][]sample, len(runs))
	for i, run := range runs {
		for j := 1; j < len(run); j++ {
			dt := (run[j].t - run[j-1].t).Seconds()
			if dt <= 0 {
				continue
			}
			v := math.Hypot(run[j].x-run[j-1].x, run[j].y-run[j-1].y) / dt
			speeds[i] = append(speeds[i], sample{run[j].t, v, 0})
		}
		// Average over 5 reports so the shape reads through the tremor.
		raw := speeds[i]
		for j := range raw {
			sum, n := 0.0, 0
			for d := -2; d <= 2; d++ {
				if j+d >= 0 && j+d < len(raw) {
					sum += raw[j+d].x
					n++
				}
			}
			speeds[i][j].x = sum / float64(n)
			maxT = math.Max(maxT, raw[j].t.Seconds())
			maxV = math.Max(maxV, speeds[i][j].x)
		}
	}
	maxT = math.Ceil(maxT*5) / 5
	maxV = math.Ceil(maxV/1000) * 1000
	sx := func(t float64) float64 { return left + t/maxT*(w-left-right) }
	sy := func(v float64) float64 { return h - bottom - v/maxV*(h-top-bottom) }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f">%s`, w, h, w, h, svgStyle)
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" rx="12" fill="#ffffff" stroke="#e2e8f0"/>`)
	fmt.Fprintf(&b, `<text x="24" y="34" class="t">Cursor speed over one move</text>`)
	fmt.Fprintf(&b, `<text x="24" y="54" class="s">Speed rises, peaks mid-way and falls, like a hand. The small bump at the end is the correction after an overshoot.</text>`)
	fmt.Fprintf(&b, `<line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" stroke="#94a3b8"/>`, left, h-bottom, w-right, h-bottom)
	fmt.Fprintf(&b, `<line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" stroke="#94a3b8"/>`, left, top, left, h-bottom)
	for ms := 0.0; ms <= maxT*1000; ms += 200 {
		x := sx(ms / 1000)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" class="s" text-anchor="middle">%.0f ms</text>`, x, h-bottom+20, ms)
	}
	for v := 0.0; v <= maxV; v += maxV / 4 {
		fmt.Fprintf(&b, `<text x="%.0f" y="%.1f" class="s" text-anchor="end">%.0f</text>`, left-8, sy(v)+4, v)
	}
	fmt.Fprintf(&b, `<text x="16" y="%.0f" class="s" transform="rotate(-90 16 %.0f)" text-anchor="middle">points / s</text>`, (top+h-bottom)/2, (top+h-bottom)/2)
	for i, sp := range speeds {
		var pts []string
		for _, s := range sp {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", sx(s.t.Seconds()), sy(s.x)))
		}
		fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2.2"/>`, strings.Join(pts, " "), palette[i])
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// animated plays three moves at real speed, each cursor leaving a trail, so
// the curve, the speed change and the overshoot can be seen in motion.
func animated(runs [][]sample, from, to Point) string {
	const w, h, pad, head = 880.0, 460.0, 50.0, 60.0
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	var longest time.Duration
	for _, run := range runs {
		for _, s := range run {
			minX, maxX = math.Min(minX, s.x), math.Max(maxX, s.x)
			minY, maxY = math.Min(minY, s.y), math.Max(maxY, s.y)
		}
		longest = max(longest, run[len(run)-1].t)
	}
	k := math.Min((w-2*pad)/(maxX-minX), (h-head-2*pad)/(maxY-minY))
	px := func(x float64) float64 { return pad + (x-minX)*k }
	py := func(y float64) float64 { return head + pad + (y-minY)*k }
	cycle := longest.Seconds() + 1.2 // hold at the target before looping

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f">%s`, w, h, w, h, svgStyle)
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" rx="12" fill="#ffffff" stroke="#e2e8f0"/>`)
	fmt.Fprintf(&b, `<text x="24" y="34" class="t">Three moves to the same target, played at real speed</text>`)
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="7" fill="#0f172a"/>`, px(from.X), py(from.Y))
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="#0f172a" stroke-width="2"/>`, px(to.X), py(to.Y), math.Max(10*k, 6))
	for i, run := range runs {
		c := palette[i%len(palette)]
		var d strings.Builder
		var lengths []float64
		total := 0.0
		for j, s := range run {
			if j == 0 {
				fmt.Fprintf(&d, "M%.1f %.1f", px(s.x), py(s.y))
			} else {
				fmt.Fprintf(&d, " L%.1f %.1f", px(s.x), py(s.y))
				total += math.Hypot(px(s.x)-px(run[j-1].x), py(s.y)-py(run[j-1].y))
			}
			lengths = append(lengths, total)
		}
		// keyTimes follow the recorded clock and keyPoints the distance
		// travelled, so the cursor speeds up and slows down as recorded.
		var times, points []string
		last := -1.0
		for j, s := range run {
			kt := s.t.Seconds() / cycle
			if kt <= last {
				continue
			}
			last = kt
			times = append(times, fmt.Sprintf("%.4f", kt))
			points = append(points, fmt.Sprintf("%.4f", lengths[j]/total))
		}
		times[0] = "0"
		times = append(times, "1")
		points = append(points, "1")
		id := fmt.Sprintf("p%d", i)
		fmt.Fprintf(&b, `<path id="%s" d="%s" fill="none" stroke="%s" stroke-width="2.5" stroke-opacity="0.85" pathLength="1" stroke-dasharray="1" stroke-dashoffset="1">`, id, d.String(), c)
		fmt.Fprintf(&b, `<animate attributeName="stroke-dashoffset" values="%s" keyTimes="%s" dur="%.2fs" repeatCount="indefinite"/></path>`,
			dashValues(points), strings.Join(times, ";"), cycle)
		fmt.Fprintf(&b, `<g><path d="M0 0 L0 17 L4.5 13 L7.5 19.5 L10 18.5 L7 12 L12.5 12 Z" fill="%s" stroke="#ffffff" stroke-width="1.2"/>`, c)
		fmt.Fprintf(&b, `<animateMotion dur="%.2fs" repeatCount="indefinite" calcMode="linear" keyPoints="%s" keyTimes="%s"><mpath href="#%s"/></animateMotion></g>`,
			cycle, strings.Join(points, ";"), strings.Join(times, ";"), id)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// dashValues turns travelled fractions into dash offsets that reveal the trail.
func dashValues(points []string) string {
	out := make([]string, len(points))
	for i, p := range points {
		var f float64
		fmt.Sscanf(p, "%f", &f)
		out[i] = fmt.Sprintf("%.4f", 1-f)
	}
	return strings.Join(out, ";")
}
