// Package human turns high-level input commands ("move to x,y", "type this")
// into HID report streams with human timing and motion.
//
// Mouse moves follow a curved path with a minimum-jerk speed profile (slow
// start, fast middle, slow end), a duration set by Fitts's law, slight tremor,
// and on long moves an overshoot followed by a short correction. The virtual
// mouse is relative-only and macOS applies pointer acceleration to it, so
// report units are not pixels: the engine reads the real cursor position, keeps
// a running estimate of the acceleration gain, and corrects toward the path.
//
// The engine shares the one macOS cursor with the person at the Mac. If the
// cursor moves in a way the engine did not command, it treats that as the
// person taking over: it stops, releases every key and button, and returns
// ErrUserTookControl.
package human

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/Lucklyric/virtual-nanokvm/internal/hid"
	"github.com/Lucklyric/virtual-nanokvm/internal/keymap"
)

// ErrUserTookControl means the real mouse moved during an action. The action
// stopped and every key and button was released.
var ErrUserTookControl = errors.New("user took control: the mouse moved by hand, action stopped")

const (
	// idleDrift is how far the cursor may move, in points, while the engine
	// is not moving it (typing, clicking, scrolling, pausing).
	idleDrift = 3.0
	// pathDrift is how far the cursor may stray from the planned path before
	// it counts as a takeover, on top of an allowance for the current speed.
	pathDrift = 40.0
)

// Point is a position in global display coordinates (points, not pixels).
type Point struct{ X, Y float64 }

// Cursor reports where the macOS cursor is.
type Cursor interface {
	Position() (Point, error)
}

// Engine drives one virtual keyboard and mouse. It is safe for concurrent use;
// commands run one at a time so streams from different callers never interleave.
type Engine struct {
	HID    hid.Backend
	Cursor Cursor
	Rand   *rand.Rand
	// Sleep waits for d or until ctx ends. Tests replace it with a fake clock.
	Sleep func(ctx context.Context, d time.Duration) error
	// Tick is the pointing report interval (8 ms is a common 125 Hz mouse).
	Tick time.Duration

	mu      sync.Mutex
	speed   float64 // multiplies the pace of moves, clicks and key presses; 0 means 1
	buttons uint32
	gain    float64 // estimated cursor points moved per report unit
	anchor  Point   // where the cursor should be while the engine holds it still

	relayLast  Point     // last point a remote hand was relayed to
	relayAt    time.Time // when
	relayFrom  Point     // where the cursor was then
	relaySentX int       // report units sent for it
	relaySentY int
}

// New returns an Engine with real time and a random seed.
func New(backend hid.Backend, cursor Cursor) *Engine {
	return &Engine{
		HID:    backend,
		Cursor: cursor,
		Rand:   rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())),
		Sleep:  sleep,
		Tick:   8 * time.Millisecond,
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Speed limits: faster than MaxSpeed, moves get too few reports to curve.
const (
	MinSpeed = 0.25
	MaxSpeed = 5.0
)

// SetSpeed sets the pace of moves, clicks and key presses: 2 is twice as
// fast as a typical person, 0.5 half as fast. Paths keep their shape and the
// takeover check stays on. Typing keeps its own words-per-minute pace. It
// returns the previous speed.
func (e *Engine) SetSpeed(s float64) (float64, error) {
	if s < MinSpeed || s > MaxSpeed {
		return 0, fmt.Errorf("speed must be %.2g to %.2g (1 is a typical person)", MinSpeed, MaxSpeed)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	prev := e.pace()
	e.speed = s
	return prev, nil
}

func (e *Engine) pace() float64 {
	if e.speed == 0 {
		return 1
	}
	return e.speed
}

func (e *Engine) uniform(lo, hi float64) float64 { return lo + e.Rand.Float64()*(hi-lo) }

// lognormal draws a duration around medianMS with spread sigma, the shape of
// human reaction and dwell times (mostly short, sometimes long), at the
// engine's speed.
func (e *Engine) lognormal(medianMS, sigma float64) time.Duration {
	return time.Duration(medianMS * math.Exp(e.Rand.NormFloat64()*sigma) / e.pace() * float64(time.Millisecond))
}

func (e *Engine) pause(ctx context.Context, loMS, hiMS float64) error {
	return e.Sleep(ctx, time.Duration(e.uniform(loMS, hiMS)/e.pace()*float64(time.Millisecond)))
}

// hold records where the cursor is, so still can tell if a hand moves it.
func (e *Engine) hold() error {
	p, err := e.Cursor.Position()
	e.anchor = p
	return err
}

// still returns ErrUserTookControl if the cursor left the held position.
func (e *Engine) still() error {
	p, err := e.Cursor.Position()
	if err != nil {
		return err
	}
	if math.Hypot(p.X-e.anchor.X, p.Y-e.anchor.Y) > idleDrift {
		return ErrUserTookControl
	}
	return nil
}

// guard finishes a public action: on a takeover it lets go of everything so
// no key or button stays down under the person's hand.
func (e *Engine) guard(err error) error {
	if errors.Is(err, ErrUserTookControl) {
		e.buttons = 0
		if rerr := e.HID.ReleaseAll(); rerr != nil {
			return errors.Join(err, rerr)
		}
	}
	return err
}

// MoveTo moves the cursor to target. targetSize is the rough size in points of
// what is being pointed at (a button, a link); smaller targets take longer, as
// with a person. It returns where the cursor ended up.
func (e *Engine) MoveTo(ctx context.Context, target Point, targetSize float64) (Point, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.moveTo(ctx, target, targetSize)
	return p, e.guard(err)
}

func (e *Engine) moveTo(ctx context.Context, target Point, targetSize float64) (Point, error) {
	if e.gain == 0 {
		e.gain = 1
	}
	if targetSize <= 0 {
		targetSize = 20
	}
	if err := e.hold(); err != nil {
		return Point{}, err
	}
	start := e.anchor
	dist := math.Hypot(target.X-start.X, target.Y-start.Y)
	if dist < 0.5 {
		return start, nil
	}
	// Reaction time: the hand starts a moment after the decision.
	if err := e.Sleep(ctx, e.lognormal(140, 0.35)); err != nil {
		return Point{}, err
	}
	if err := e.still(); err != nil {
		return Point{}, err
	}

	// Each move picks one way to approach the target, as people vary:
	//   overshoot  the first stroke flies a little past, then comes back
	//   undershoot the first stroke falls short, a second one finishes
	//   detour     on long moves the hand bends through a point off the line
	//   direct     one stroke straight onto the target
	ux, uy := (target.X-start.X)/dist, (target.Y-start.Y)/dist
	side := e.Rand.NormFloat64() * dist * 0.01
	aim := target
	var via *Point
	switch r := e.Rand.Float64(); {
	case dist > 400 && r < 0.12:
		off := dist * e.uniform(0.12, 0.25)
		if e.Rand.Float64() < 0.5 {
			off = -off
		}
		at := e.uniform(0.4, 0.6)
		via = &Point{start.X + ux*dist*at - uy*off, start.Y + uy*dist*at + ux*off}
	case dist > 150 && r < 0.47:
		over := dist * e.uniform(0.02, 0.06)
		aim = Point{target.X + ux*over - uy*side, target.Y + uy*over + ux*side}
	case dist > 60 && r < 0.75:
		short := dist * e.uniform(0.05, 0.12)
		aim = Point{target.X - ux*short - uy*side, target.Y - uy*short + ux*side}
	}

	from := start
	if via != nil {
		d := math.Hypot(via.X-start.X, via.Y-start.Y)
		if err := e.segment(ctx, start, *via, e.fitts(d, targetSize)*3/4); err != nil {
			return Point{}, err
		}
		from = *via
	}
	if err := e.segment(ctx, from, aim, e.fitts(math.Hypot(aim.X-from.X, aim.Y-from.Y), targetSize)); err != nil {
		return Point{}, err
	}
	if aim != target {
		if err := e.hold(); err != nil {
			return Point{}, err
		}
		if err := e.pause(ctx, 30, 90); err != nil {
			return Point{}, err
		}
		if err := e.still(); err != nil {
			return Point{}, err
		}
		from := e.anchor
		short := math.Hypot(target.X-from.X, target.Y-from.Y)
		if err := e.segment(ctx, from, target, e.fitts(short, targetSize)); err != nil {
			return Point{}, err
		}
	}
	return e.settle(ctx, target)
}

// fitts returns a movement time from Fitts's law, T = a + b*log2(D/W + 1),
// with per-move variation.
func (e *Engine) fitts(dist, width float64) time.Duration {
	sec := (0.08 + 0.13*math.Log2(dist/width+1)) * e.uniform(0.85, 1.15) / e.pace()
	return time.Duration(sec * float64(time.Second))
}

// minimumJerk maps normalized time to normalized distance: the bell-shaped
// speed profile of human reaching movements.
func minimumJerk(t float64) float64 {
	return t * t * t * (10 - 15*t + 6*t*t)
}

func bezier(p0, p1, p2, p3 Point, s float64) Point {
	u := 1 - s
	a, b, c, d := u*u*u, 3*u*u*s, 3*u*s*s, s*s*s
	return Point{
		a*p0.X + b*p1.X + c*p2.X + d*p3.X,
		a*p0.Y + b*p1.Y + c*p2.Y + d*p3.Y,
	}
}

// segment moves along one curved stroke from `from` to `to` over dur.
func (e *Engine) segment(ctx context.Context, from, to Point, dur time.Duration) error {
	dx, dy := to.X-from.X, to.Y-from.Y
	dist := math.Hypot(dx, dy)
	if dist < 0.5 {
		return nil
	}
	// Bend the path to one side, like a wrist or elbow arc.
	nx, ny := -dy/dist, dx/dist
	bend := dist * e.uniform(0.04, 0.2)
	if e.Rand.Float64() < 0.5 {
		bend = -bend
	}
	t1, t2 := e.uniform(0.2, 0.4), e.uniform(0.6, 0.8)
	c1 := Point{from.X + dx*t1 + nx*bend*e.uniform(0.6, 1.2), from.Y + dy*t1 + ny*bend*e.uniform(0.6, 1.2)}
	c2 := Point{from.X + dx*t2 + nx*bend*e.uniform(0.6, 1.2), from.Y + dy*t2 + ny*bend*e.uniform(0.6, 1.2)}

	steps := max(int(math.Ceil(float64(dur)/float64(e.Tick))), 2)
	prev := from
	var tremX, tremY, carryX, carryY float64
	strikes := 0
	lastActual := from
	for i := 1; i <= steps; i++ {
		tau := float64(i) / float64(steps)
		// Smoothed tremor that fades out as the hand homes in.
		tremX = 0.7*tremX + e.Rand.NormFloat64()*0.35
		tremY = 0.7*tremY + e.Rand.NormFloat64()*0.35
		fade := 1 - tau
		next := bezier(from, c1, c2, to, minimumJerk(tau))
		next.X += tremX * fade
		next.Y += tremY * fade

		actual, err := e.Cursor.Position()
		if err != nil {
			return err
		}
		// A hand shows up as the cursor both off the path and moving in a
		// way the path does not explain. Being off the path alone is not
		// enough: a cursor pinned at a screen edge is off the path too, but
		// barely moves. Positions lag about a tick, so compare against the
		// previous path point; two strikes in a row rule out one late reading.
		stepX, stepY := next.X-prev.X, next.Y-prev.Y
		speed := math.Hypot(stepX, stepY)
		offPath := math.Hypot(actual.X-prev.X, actual.Y-prev.Y) > pathDrift+3*speed
		unexplained := math.Hypot(actual.X-lastActual.X-stepX, actual.Y-lastActual.Y-stepY) > pathDrift
		lastActual = actual
		if offPath && unexplained {
			if strikes++; strikes >= 2 {
				return ErrUserTookControl
			}
		} else {
			strikes = 0
		}
		// Feed-forward along the path, plus a gentle pull back toward where
		// the cursor should already be (gentle because positions lag a tick).
		wantX := (next.X - prev.X) + 0.3*(prev.X-actual.X)
		wantY := (next.Y - prev.Y) + 0.3*(prev.Y-actual.Y)
		sx, sy := e.units(wantX, &carryX), e.units(wantY, &carryY)
		if sx != 0 || sy != 0 {
			if err := e.HID.Pointer(e.buttons, sx, sy, 0, 0); err != nil {
				return err
			}
		}
		prev = next
		if err := e.Sleep(ctx, e.Tick); err != nil {
			return err
		}
		e.learnGain(actual, sx, sy)
	}
	return nil
}

// units converts a wanted move in points to report units, carrying the
// fractional remainder so slow movement still adds up.
func (e *Engine) units(points float64, carry *float64) int {
	v := points/e.gain + *carry
	n := math.Round(v)
	n = math.Max(-hid.MaxDelta, math.Min(hid.MaxDelta, n))
	*carry = v - n
	return int(n)
}

// learnGain updates the acceleration estimate from how far the cursor moved
// after the previous report.
func (e *Engine) learnGain(before Point, sx, sy int) {
	sent := math.Hypot(float64(sx), float64(sy))
	if sent < 3 {
		return
	}
	after, err := e.Cursor.Position()
	if err != nil {
		return
	}
	moved := math.Hypot(after.X-before.X, after.Y-before.Y)
	ratio := math.Max(0.2, math.Min(5, moved/sent))
	e.gain = 0.85*e.gain + 0.15*ratio
}

// settle makes small corrective moves until the cursor is on target.
func (e *Engine) settle(ctx context.Context, target Point) (Point, error) {
	stuck := 0
	last, err := e.Cursor.Position()
	if err != nil {
		return Point{}, err
	}
	prevErr := math.Inf(1)
	for range 40 {
		ex, ey := target.X-last.X, target.Y-last.Y
		if math.Abs(ex) < 1 && math.Abs(ey) < 1 {
			return last, nil
		}
		// Corrections only shrink the error; a jump away is a hand.
		dist := math.Hypot(ex, ey)
		if dist > prevErr+pathDrift {
			return last, ErrUserTookControl
		}
		prevErr = dist
		sx, sy := corrective(ex, e.gain), corrective(ey, e.gain)
		if err := e.HID.Pointer(e.buttons, sx, sy, 0, 0); err != nil {
			return Point{}, err
		}
		// Wait two ticks so the position read reflects this report.
		if err := e.Sleep(ctx, 2*e.Tick); err != nil {
			return Point{}, err
		}
		p, err := e.Cursor.Position()
		if err != nil {
			return Point{}, err
		}
		if math.Hypot(p.X-last.X, p.Y-last.Y) < 0.01 {
			// Pinned against a screen edge, or the target is off screen.
			if stuck++; stuck >= 5 {
				return p, fmt.Errorf("cursor stopped at (%.0f, %.0f) short of (%.0f, %.0f)", p.X, p.Y, target.X, target.Y)
			}
		} else {
			stuck = 0
		}
		last = p
	}
	if off := math.Hypot(target.X-last.X, target.Y-last.Y); off > settleTolerance {
		return last, fmt.Errorf("cursor stopped %.1f pt short of (%.0f, %.0f) after its corrections; nothing was clicked", off, target.X, target.Y)
	}
	return last, nil
}

// settleTolerance is how far from the target, in points, the cursor may end
// once the corrections run out. Within one pixel either way is a hit.
const settleTolerance = 2.0

// corrective sizes a correction: about half the remaining error, at least one
// unit, so it converges without bouncing.
func corrective(errPoints, gain float64) int {
	if math.Abs(errPoints) < 1 {
		return 0
	}
	n := math.Round(0.5 * errPoints / gain)
	if n == 0 {
		n = math.Copysign(1, errPoints)
	}
	return int(math.Max(-hid.MaxDelta, math.Min(hid.MaxDelta, n)))
}

// Button identifies a mouse button.
type Button uint32

const (
	Left   = Button(hid.ButtonLeft)
	Right  = Button(hid.ButtonRight)
	Middle = Button(hid.ButtonMiddle)
)

// Click presses and releases button count times (2 for a double click).
func (e *Engine) Click(ctx context.Context, b Button, count int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.guard(e.click(ctx, b, count))
}

func (e *Engine) click(ctx context.Context, b Button, count int) error {
	if e.gain == 0 {
		e.gain = 1
	}
	if err := e.hover(ctx); err != nil {
		return err
	}
	for i := range count {
		if err := e.still(); err != nil {
			return err
		}
		if err := e.press(ctx, b); err != nil {
			return err
		}
		if err := e.pause(ctx, 55, 110); err != nil {
			return err
		}
		if err := e.release(b); err != nil {
			return err
		}
		if i < count-1 {
			if err := e.pause(ctx, 70, 130); err != nil {
				return err
			}
		}
	}
	return e.relax(ctx)
}

// hover is the moment before a click: a dwell (mostly short, sometimes a
// longer look), and now and then a tiny wobble off the spot and back.
func (e *Engine) hover(ctx context.Context) error {
	if err := e.hold(); err != nil {
		return err
	}
	dwell := e.lognormal(130, 0.5)
	if e.Rand.Float64() < 0.15 {
		dwell += e.lognormal(350, 0.4) // reading the label before committing
	}
	if e.Rand.Float64() < 0.25 {
		spot := e.anchor
		a := e.uniform(0, 2*math.Pi)
		r := e.uniform(1, 2.5)
		off := Point{spot.X + r*math.Cos(a), spot.Y + r*math.Sin(a)}
		if err := e.segment(ctx, spot, off, e.lognormal(70, 0.3)); err != nil {
			return err
		}
		if err := e.segment(ctx, off, spot, e.lognormal(70, 0.3)); err != nil {
			return err
		}
		if _, err := e.settle(ctx, spot); err != nil {
			return err
		}
		if err := e.hold(); err != nil {
			return err
		}
	}
	if err := e.Sleep(ctx, dwell); err != nil {
		return err
	}
	return e.still()
}

// relax is the hand after a click: often it drifts a few points away as the
// finger lifts, sometimes it stays put.
func (e *Engine) relax(ctx context.Context) error {
	if e.Rand.Float64() >= 0.45 {
		return nil
	}
	if err := e.pause(ctx, 40, 140); err != nil {
		return err
	}
	if err := e.hold(); err != nil {
		return err
	}
	a := e.uniform(0, 2*math.Pi)
	r := e.uniform(3, 12)
	to := Point{e.anchor.X + r*math.Cos(a), e.anchor.Y + r*math.Sin(a)}
	return e.segment(ctx, e.anchor, to, e.lognormal(180, 0.3))
}

func (e *Engine) press(ctx context.Context, b Button) error {
	e.buttons |= uint32(b)
	return e.HID.Pointer(e.buttons, 0, 0, 0, 0)
}

func (e *Engine) release(b Button) error {
	e.buttons &^= uint32(b)
	return e.HID.Pointer(e.buttons, 0, 0, 0, 0)
}

// Drag presses button at the current position, moves to target and releases.
func (e *Engine) Drag(ctx context.Context, b Button, target Point) (Point, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.press(ctx, b); err != nil {
		return Point{}, err
	}
	var p Point
	err := e.pause(ctx, 80, 150)
	if err == nil {
		p, err = e.moveTo(ctx, target, 20)
	}
	if err == nil {
		err = e.pause(ctx, 60, 120)
	}
	// Always let go of the button, even when the move failed.
	if rerr := e.release(b); err == nil {
		err = rerr
	}
	return p, e.guard(err)
}

// Scroll turns the wheel. Positive dy scrolls down (content moves up),
// positive dx scrolls right. Units are wheel notches.
func (e *Engine) Scroll(ctx context.Context, dx, dy int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.guard(e.scroll(ctx, dx, dy))
}

func (e *Engine) scroll(ctx context.Context, dx, dy int) error {
	if err := e.hold(); err != nil {
		return err
	}
	for dx != 0 || dy != 0 {
		if err := e.still(); err != nil {
			return err
		}
		v, h := -sign(dy), sign(dx)
		if err := e.HID.Pointer(e.buttons, 0, 0, v, h); err != nil {
			return err
		}
		dy -= sign(dy)
		dx -= sign(dx)
		// Notches come in quick bursts with short breaks, like a finger flick.
		lo, hi := 18.0, 45.0
		if e.Rand.Float64() < 0.15 {
			lo, hi = 90, 200
		}
		if err := e.pause(ctx, lo, hi); err != nil {
			return err
		}
	}
	return nil
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

// Type types text at roughly wpm words per minute with human rhythm. It
// validates every character before pressing anything.
func (e *Engine) Type(ctx context.Context, text string, wpm float64) error {
	strokes := make([]keymap.Stroke, 0, len(text))
	runes := []rune(text)
	for _, r := range runes {
		s, err := keymap.Char(r)
		if err != nil {
			return err
		}
		strokes = append(strokes, s)
	}
	if wpm <= 0 {
		wpm = 70
	}
	// One word is five characters; inter-key gaps are log-normal around the mean.
	meanMS := 60000 / (wpm * 5)

	e.mu.Lock()
	defer e.mu.Unlock()
	return e.guard(e.typeStrokes(ctx, strokes, runes, meanMS))
}

func (e *Engine) typeStrokes(ctx context.Context, strokes []keymap.Stroke, runes []rune, meanMS float64) error {
	if err := e.hold(); err != nil {
		return err
	}
	for i, s := range strokes {
		if err := e.still(); err != nil {
			return err
		}
		if err := e.stroke(ctx, s); err != nil {
			return err
		}
		if i == len(strokes)-1 {
			break
		}
		gap := meanMS * math.Exp(e.Rand.NormFloat64()*0.35-0.06)
		switch runes[i] {
		case ' ', '.', ',', '\n', ';', ':', '!', '?':
			gap += e.uniform(0, 140)
		}
		if err := e.Sleep(ctx, time.Duration(gap*float64(time.Millisecond))); err != nil {
			return err
		}
	}
	return nil
}

// stroke presses one key, holding shift first when the key needs it.
func (e *Engine) stroke(ctx context.Context, s keymap.Stroke) error {
	if s.Modifiers != 0 {
		if err := e.HID.Keyboard(s.Modifiers, nil); err != nil {
			return err
		}
		if err := e.pause(ctx, 15, 45); err != nil {
			return err
		}
	}
	if err := e.HID.Keyboard(s.Modifiers, []uint16{s.Usage}); err != nil {
		return err
	}
	if err := e.pause(ctx, 40, 95); err != nil {
		return err
	}
	if s.Modifiers != 0 {
		if err := e.HID.Keyboard(s.Modifiers, nil); err != nil {
			return err
		}
		if err := e.pause(ctx, 10, 35); err != nil {
			return err
		}
	}
	return e.HID.Keyboard(0, nil)
}

// Press performs a key combination such as "cmd+c" or "enter": modifiers go
// down one by one, the key is tapped, and modifiers come up in reverse.
func (e *Engine) Press(ctx context.Context, spec string) error {
	mods, key, err := keymap.Combo(spec)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.guard(e.combo(ctx, mods, key))
}

// Hold keeps keys down for d, then lets go: walking in a game, holding an
// arrow to scroll. keys is one or more combinations separated by spaces, held
// together ("w", "up right", "shift+right"). At most 6 keys.
func (e *Engine) Hold(ctx context.Context, keys string, d time.Duration) error {
	var mods uint8
	var usages []uint16
	for _, spec := range strings.Fields(keys) {
		ms, key, err := keymap.Combo(spec)
		if err != nil {
			return err
		}
		for _, m := range ms {
			mods |= m
		}
		mods |= key.Modifiers
		usages = append(usages, key.Usage)
	}
	if len(usages) == 0 || len(usages) > 6 {
		return fmt.Errorf("hold 1 to 6 keys, separated by spaces")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.guard(e.holdKeys(ctx, mods, usages, d))
}

func (e *Engine) holdKeys(ctx context.Context, mods uint8, usages []uint16, d time.Duration) error {
	if err := e.hold(); err != nil {
		return err
	}
	if mods != 0 {
		if err := e.HID.Keyboard(mods, nil); err != nil {
			return err
		}
		if err := e.pause(ctx, 25, 60); err != nil {
			return err
		}
	}
	if err := e.HID.Keyboard(mods, usages); err != nil {
		return err
	}
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if err := sleepCtx(ctx, min(50*time.Millisecond, time.Until(end))); err != nil {
			return err
		}
		if err := e.still(); err != nil {
			return err
		}
	}
	if err := e.HID.Keyboard(mods, nil); err != nil {
		return err
	}
	return e.HID.Keyboard(0, nil)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (e *Engine) combo(ctx context.Context, mods []uint8, key keymap.Stroke) error {
	if err := e.hold(); err != nil {
		return err
	}
	var held uint8
	for _, m := range mods {
		held |= m
		if err := e.HID.Keyboard(held, nil); err != nil {
			return err
		}
		if err := e.pause(ctx, 25, 60); err != nil {
			return err
		}
	}
	if err := e.still(); err != nil {
		return err
	}
	if err := e.HID.Keyboard(key.Modifiers, []uint16{key.Usage}); err != nil {
		return err
	}
	if err := e.pause(ctx, 60, 120); err != nil {
		return err
	}
	if err := e.HID.Keyboard(held, nil); err != nil {
		return err
	}
	for i := len(mods) - 1; i >= 0; i-- {
		if err := e.pause(ctx, 20, 50); err != nil {
			return err
		}
		held &^= mods[i]
		if err := e.HID.Keyboard(held, nil); err != nil {
			return err
		}
	}
	return nil
}
