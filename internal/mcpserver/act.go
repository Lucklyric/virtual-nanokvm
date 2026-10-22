package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Lucklyric/virtual-nanokvm/internal/human"
)

const (
	maxSteps        = 20
	maxTypedChars   = 2000
	previewWidth    = 640 // main-display previews used to see whether the screen moved
	stillPixels     = 20  // fewer changed preview pixels than this counts as still (a blinking caret)
	minRegionChange = 6   // the fewest changed pixels that count as a change in a small region
	changePixels    = 40  // more than this counts as a change
	settleTimeout   = 1500 * time.Millisecond
)

// step is one action in a sequence. Coordinates are pixels of a shot.
type step struct {
	Do     string    `json:"do" jsonschema:"move, click, double_click, right_click, drag, scroll, type, key, hold, menu, wait, wait_for or expect"`
	Shot   int       `json:"shot,omitempty" jsonschema:"shot id the x, y come from (defaults to the sequence's shot)"`
	X      *float64  `json:"x,omitempty" jsonschema:"pixel x in that shot; move, drag: required; clicks: move here first, omit to click in place"`
	Y      *float64  `json:"y,omitempty" jsonschema:"pixel y in that shot"`
	Size   float64   `json:"size,omitempty" jsonschema:"rough target size in that shot's pixels; small targets get a slower, careful approach"`
	Text   string    `json:"text,omitempty" jsonschema:"type: the text (US keyboard characters)"`
	WPM    float64   `json:"wpm,omitempty" jsonschema:"type: words per minute (default 70)"`
	Keys   string    `json:"keys,omitempty" jsonschema:"key: a key or combination, e.g. enter, cmd+a, cmd+shift+4; hold: keys held together, separated by spaces, e.g. w or up right"`
	Menu   string    `json:"menu,omitempty" jsonschema:"menu: a menu bar path of the shot's app, e.g. Edit > Select All (works in background, where cmd shortcuts do not arrive)"`
	DX     int       `json:"dx,omitempty" jsonschema:"scroll: wheel notches, positive scrolls right"`
	DY     int       `json:"dy,omitempty" jsonschema:"scroll: wheel notches, positive scrolls down"`
	MS     int       `json:"ms,omitempty" jsonschema:"wait: milliseconds; hold: how long to hold the keys; wait_for, expect: timeout (default 3000, max 15000)"`
	Until  string    `json:"until,omitempty" jsonschema:"wait_for, expect: change (the screen changed since before the previous step) or stable (it stopped moving)"`
	Region []float64 `json:"region,omitempty" jsonschema:"wait_for, expect: watch only this x, y, width, height in the shot's pixels"`
}

// Background acts on one app without the cursor: controls are pressed
// through Accessibility, keys and the wheel go to the app's process.
type Background interface {
	// Act presses the control of app pid at global (x, y), focuses it when it
	// is a text field, or opens its context menu. It describes what it did.
	Act(ctx context.Context, pid int, x, y float64, menu bool) (string, error)
	// Menu chooses a menu bar item of app pid, such as "Edit > Select All".
	Menu(pid int, path string) error
	// Engine returns an engine whose keyboard and wheel reach only app pid;
	// the wheel lands at global (x, y).
	Engine(pid int, x, y float64) *human.Engine
}

// runner executes steps against the engine and screen.
type runner struct {
	e    *human.Engine
	scr  Screen
	reg  *shots
	last *shot      // shot of the last coordinate step, for the observation
	bg   Background // set in background mode
	ax   Background // Accessibility, for menus in either mode; may be nil

	def    *shot         // the sequence's shot
	maxAge time.Duration // refuse coordinates from shots older than this; 0: any age

	before    []byte // preview taken before the current step, for a change after it
	beforeErr error

	expects, misses int // expect steps run, and those whose prediction failed
}

// validate checks a sequence before anything moves, so a typo in step 5
// does not leave the first four done.
func (r *runner) validate(steps []step, defShot int) error {
	if r.bg != nil {
		sh := r.reg.latest()
		if defShot != 0 {
			sh, _ = r.reg.get(defShot)
		}
		for _, s := range steps {
			if s.X != nil && s.Shot == 0 {
				s.Shot = defShot
			}
			if s.X != nil {
				var err error
				if sh, err = r.reg.get(s.Shot); err != nil {
					return err
				}
			}
			if sh != nil && sh.Window == 0 {
				return fmt.Errorf("background mode works on one app: use a shot taken with app or window_id")
			}
			switch s.Do {
			case "double_click", "drag":
				return fmt.Errorf("%s is not available in background mode; use foreground, or a key such as enter", s.Do)
			case "right_click":
				return fmt.Errorf("right_click would bring the app to the front; use foreground, or a menu step")
			}
		}
		if sh == nil {
			return fmt.Errorf("background mode needs a shot of the app first (screenshot with app)")
		}
		r.last = sh
	}
	if len(steps) == 0 {
		return fmt.Errorf("no steps")
	}
	if defShot != 0 {
		sh, err := r.reg.get(defShot)
		if err != nil {
			return err
		}
		r.def = sh
	}
	if len(steps) > maxSteps {
		return fmt.Errorf("%d steps; at most %d per call", len(steps), maxSteps)
	}
	typed := 0
	for i := range steps {
		s := &steps[i]
		if s.Shot == 0 {
			s.Shot = defShot
		}
		where := fmt.Sprintf("step %d (%s)", i+1, s.Do)
		if (s.X == nil) != (s.Y == nil) {
			return fmt.Errorf("%s: give both x and y, or neither", where)
		}
		switch s.Do {
		case "move", "drag":
			if s.X == nil {
				return fmt.Errorf("%s: needs x and y", where)
			}
		case "click", "double_click", "right_click", "scroll":
		case "type":
			if s.Text == "" {
				return fmt.Errorf("%s: needs text", where)
			}
			typed += len(s.Text)
		case "key":
			if s.Keys == "" {
				return fmt.Errorf("%s: needs keys", where)
			}
		case "wait":
			if s.MS <= 0 || s.MS > 15000 {
				return fmt.Errorf("%s: ms must be 1..15000", where)
			}
		case "hold":
			if s.Keys == "" || s.MS <= 0 || s.MS > 10000 {
				return fmt.Errorf("%s: needs keys and ms (1..10000)", where)
			}
		case "menu":
			if s.Menu == "" {
				return fmt.Errorf("%s: needs menu, e.g. Edit > Select All", where)
			}
		case "wait_for", "expect":
			if s.Until != "change" && s.Until != "stable" {
				return fmt.Errorf("%s: until must be change or stable", where)
			}
			if s.MS < 0 || s.MS > 15000 {
				return fmt.Errorf("%s: ms must be at most 15000", where)
			}
			if i == 0 && s.Until == "change" {
				return fmt.Errorf("%s: a change is watched from before the step ahead of it; put the action first", where)
			}
			if len(s.Region) != 0 {
				if len(s.Region) != 4 || s.Region[2] <= 0 || s.Region[3] <= 0 {
					return fmt.Errorf("%s: region is [x, y, width, height] in the shot's pixels", where)
				}
				if _, err := r.reg.get(s.Shot); err != nil {
					return fmt.Errorf("%s: %v", where, err)
				}
			}
		default:
			return fmt.Errorf("%s: unknown action", where)
		}
		if s.X != nil {
			sh, err := r.reg.get(s.Shot)
			if err != nil {
				return fmt.Errorf("%s: %v", where, err)
			}
			if err := r.fresh(sh); err != nil {
				return fmt.Errorf("%s: %v", where, err)
			}
		}
	}
	if typed > maxTypedChars {
		return fmt.Errorf("%d characters to type; at most %d per call", typed, maxTypedChars)
	}
	return nil
}

// run executes steps in order and stops at the first error. It returns one
// line per step that ran.
func (r *runner) run(ctx context.Context, steps []step) ([]string, error) {
	var done []string
	for i, s := range steps {
		var before []byte // the frame before this step, for a change wait after it
		var beforeErr error
		if i+1 < len(steps) && (steps[i+1].Do == "wait_for" || steps[i+1].Do == "expect") && steps[i+1].Until == "change" {
			before, beforeErr = r.peek(ctx)
		}
		line, err := r.one(ctx, s)
		r.before, r.beforeErr = before, beforeErr
		label := fmt.Sprintf("%d. %s", i+1, s.Do)
		if err != nil {
			done = append(done, label+": "+err.Error())
			return done, fmt.Errorf("step %d (%s) failed: %w", i+1, s.Do, err)
		}
		done = append(done, label+line)
	}
	return done, nil
}

func (r *runner) one(ctx context.Context, s step) (string, error) {
	if r.bg != nil {
		return r.background(ctx, s)
	}
	var p human.Point
	var size float64
	var sh *shot
	if s.X != nil {
		var err error
		if sh, err = r.reg.get(s.Shot); err != nil {
			return "", err
		}
		if err := r.fresh(sh); err != nil {
			return "", err
		}
		if p, err = aim(r.scr, sh, *s.X, *s.Y); err != nil {
			return "", err
		}
		size = s.Size * sh.Scale
		r.last = sh
	}
	at := ""
	if sh != nil {
		at = fmt.Sprintf(" at shot %d (%.0f, %.0f) = global (%.0f, %.0f), shot %s old", sh.ID, *s.X, *s.Y, p.X, p.Y, age(sh))
	}
	// moveTo moves to p and reports where the cursor landed.
	moveTo := func() error {
		got, err := r.e.MoveTo(ctx, p, size)
		if err == nil {
			if off := math.Hypot(got.X-p.X, got.Y-p.Y); off >= 0.5 {
				at += fmt.Sprintf(", landed %.1f pt off at (%.0f, %.0f)", off, got.X, got.Y)
			}
		}
		return err
	}
	switch s.Do {
	case "move":
		return at, moveTo()
	case "click", "double_click", "right_click":
		b, count := human.Left, 1
		if s.Do == "double_click" {
			count = 2
		}
		if s.Do == "right_click" {
			b = human.Right
		}
		if sh != nil {
			if err := moveTo(); err != nil {
				return at, err
			}
		}
		return at, r.e.Click(ctx, b, count)
	case "drag":
		_, err := r.e.Drag(ctx, human.Left, p)
		return " to" + at, err
	case "scroll":
		if sh != nil {
			if err := moveTo(); err != nil {
				return at, err
			}
		}
		return fmt.Sprintf("%s dx %d dy %d", at, s.DX, s.DY), r.e.Scroll(ctx, s.DX, s.DY)
	case "type":
		return fmt.Sprintf(" %d characters", len(s.Text)), r.e.Type(ctx, s.Text, s.WPM)
	case "key":
		return " " + s.Keys, r.e.Press(ctx, s.Keys)
	case "hold":
		return fmt.Sprintf(" %s for %d ms", s.Keys, s.MS), r.e.Hold(ctx, s.Keys, time.Duration(s.MS)*time.Millisecond)
	case "menu":
		return r.menu(s.Menu)
	}
	return r.one2(ctx, s)
}

// background runs one step on the app of the current shot, without the cursor.
func (r *runner) background(ctx context.Context, s step) (string, error) {
	sh := r.last
	var p human.Point
	at := ""
	if s.X != nil {
		var err error
		if sh, err = r.reg.get(s.Shot); err != nil {
			return "", err
		}
		if err := r.fresh(sh); err != nil {
			return "", err
		}
		// Covered points are fine here: Accessibility hit-tests inside the app.
		if p, err = toGlobal(r.scr, sh, *s.X, *s.Y); err != nil {
			return "", err
		}
		r.last = sh
		at = fmt.Sprintf(" at shot %d (%.0f, %.0f), shot %s old", sh.ID, *s.X, *s.Y, age(sh))
	} else {
		// No point given: the wheel lands in the middle of the window.
		var err error
		if p, err = toGlobal(r.scr, sh, float64(sh.W)/2, float64(sh.H)/2); err != nil {
			return "", err
		}
	}
	switch s.Do {
	case "move":
		return at + " (skipped: background mode has no cursor)", nil
	case "click", "right_click":
		if s.X == nil {
			return "", fmt.Errorf("background clicks need x and y")
		}
		did, err := r.bg.Act(ctx, sh.PID, p.X, p.Y, s.Do == "right_click")
		return at + ": " + did, err
	case "scroll":
		return fmt.Sprintf("%s dx %d dy %d", at, s.DX, s.DY), r.bg.Engine(sh.PID, p.X, p.Y).Scroll(ctx, s.DX, s.DY)
	case "type":
		wpm := s.WPM
		if wpm == 0 {
			wpm = 250 // nobody watches a background app type
		}
		return fmt.Sprintf(" %d characters", len(s.Text)), r.bg.Engine(sh.PID, p.X, p.Y).Type(ctx, s.Text, wpm)
	case "key":
		return " " + s.Keys, r.bg.Engine(sh.PID, p.X, p.Y).Press(ctx, s.Keys)
	case "hold":
		return fmt.Sprintf(" %s for %d ms", s.Keys, s.MS), r.bg.Engine(sh.PID, p.X, p.Y).Hold(ctx, s.Keys, time.Duration(s.MS)*time.Millisecond)
	case "menu":
		return r.menu(s.Menu)
	}
	return r.one2(ctx, s)
}

// menu chooses a menu bar item of the current shot's app, in either mode.
func (r *runner) menu(path string) (string, error) {
	sh := r.last
	if sh == nil {
		sh = r.reg.latest()
	}
	if sh == nil || sh.Window == 0 {
		return "", fmt.Errorf("menu needs a shot of the app first (screenshot with app)")
	}
	if r.ax == nil {
		return "", fmt.Errorf("menus need the Accessibility permission")
	}
	return " " + path, r.ax.Menu(sh.PID, path)
}

// one2 runs the steps that are the same in both modes: wait, wait_for and
// expect. expect is a wait_for that stops the sequence when the predicted
// change does not come, so the steps planned after it never run on a wrong
// screen.
func (r *runner) one2(ctx context.Context, s step) (string, error) {
	switch s.Do {
	case "wait":
		return fmt.Sprintf(" %d ms", s.MS), sleep(ctx, time.Duration(s.MS)*time.Millisecond)
	case "wait_for", "expect":
		timeout := 3 * time.Second
		if s.MS > 0 {
			timeout = time.Duration(s.MS) * time.Millisecond
		}
		var region *[4]float64
		if len(s.Region) == 4 {
			a, err := r.regionArea(s)
			if err != nil {
				return "", err
			}
			region = &a
		}
		var line string
		var ok bool
		if s.Until == "stable" {
			line, ok = r.settle(ctx, timeout, region)
		} else {
			line, ok = r.waitChange(ctx, r.before, r.beforeErr, timeout, region)
		}
		if s.Do == "expect" && strings.Contains(line, "cannot watch") {
			return line, fmt.Errorf("expect could not watch the window, so the prediction was not checked: %s", strings.Trim(strings.TrimSpace(line), "()"))
		}
		if s.Do == "expect" {
			r.expects++
			if !ok {
				r.misses++
				return line, fmt.Errorf("prediction missed: %s", strings.TrimSpace(strings.Trim(line, " ()")))
			}
		}
		return line, nil
	}
	if r.bg != nil {
		return "", fmt.Errorf("%s is not available in background mode", s.Do)
	}
	return "", fmt.Errorf("unknown action %q", s.Do)
}

// regionArea maps a step's region (pixels of its shot) to fractions of what
// peek watches: the shot's window, or the main display for a display shot.
func (r *runner) regionArea(s step) ([4]float64, error) {
	sh, err := r.reg.get(s.Shot)
	if err != nil {
		return [4]float64{}, err
	}
	if _, _, _, err := frameNow(r.scr, sh); err != nil {
		return [4]float64{}, err
	}
	if w := r.watched(); w == nil || w.Window != sh.Window {
		return [4]float64{}, fmt.Errorf("the region's shot %d is not of the window being watched", sh.ID)
	}
	x, y := sh.QX+s.Region[0]*sh.Scale, sh.QY+s.Region[1]*sh.Scale // frame points
	w, h := s.Region[2]*sh.Scale, s.Region[3]*sh.Scale
	return [4]float64{x / sh.FW, y / sh.FH, w / sh.FW, h / sh.FH}, nil
}

// watched is the shot whose frame the waits watch: the last one acted on,
// else the sequence's shot, else the latest.
func (r *runner) watched() *shot {
	switch {
	case r.last != nil:
		return r.last
	case r.def != nil:
		return r.def
	}
	return r.reg.latest()
}

// peek is a small, fast picture of what the agent is looking at, to see
// whether it moved: the window itself (on any display, even when covered),
// or the main display for a display shot. It always watches the same frame
// the screenshots come from.
func (r *runner) peek(ctx context.Context) ([]byte, error) {
	sh := r.watched()
	if sh == nil || sh.Window == 0 {
		return r.scr.Preview(previewWidth)
	}
	if _, err := r.scr.FindWindow(sh.Window, ""); err != nil {
		return nil, fmt.Errorf("window %d is gone", sh.Window)
	}
	raw, err := r.scr.Capture(ctx, sh.Window)
	if err != nil {
		return nil, fmt.Errorf("window %d could not be captured: %v", sh.Window, err)
	}
	rb := raw.Bounds()
	w := min(previewWidth, rb.Dx())
	h := max(1, rb.Dy()*w/rb.Dx())
	return encodeJPEG(shrink(raw, rb, w, h))
}

var whole = [4]float64{0, 0, 1, 1}

// settle waits until the frame (or region, fractions of it) stops moving, or
// timeout. ok is false when it was still moving, or could not be watched.
func (r *runner) settle(ctx context.Context, timeout time.Duration, region *[4]float64) (string, bool) {
	start := time.Now()
	area := whole
	if region != nil {
		area = *region
	}
	if err := sleep(ctx, 120*time.Millisecond); err != nil {
		return "", false
	}
	prev, err := r.peek(ctx)
	if err != nil {
		return fmt.Sprintf(" (cannot watch: %v)", err), false
	}
	for time.Since(start) < timeout {
		if sleep(ctx, 100*time.Millisecond) != nil {
			break
		}
		cur, err := r.peek(ctx)
		if err != nil {
			return fmt.Sprintf(" (cannot watch: %v)", err), false
		}
		if n, _ := changed(prev, cur, area); n < stillPixels {
			return fmt.Sprintf(" (still after %d ms)", time.Since(start).Milliseconds()), true
		}
		prev = cur
	}
	return fmt.Sprintf(" (timed out: still moving after %d ms)", timeout.Milliseconds()), false
}

// waitChange waits until the frame (or region) differs from before, or
// timeout. ok is false when nothing changed, or it could not be watched.
func (r *runner) waitChange(ctx context.Context, before []byte, beforeErr error, timeout time.Duration, region *[4]float64) (string, bool) {
	start := time.Now()
	if beforeErr != nil || before == nil {
		return fmt.Sprintf(" (cannot watch: %v)", beforeErr), false
	}
	area := whole
	if region != nil {
		area = *region
	}
	for time.Since(start) < timeout {
		cur, err := r.peek(ctx)
		if err != nil {
			return fmt.Sprintf(" (cannot watch: %v)", err), false
		}
		need := changePixels
		if region != nil {
			// In a small region an eighth of it is a change, but never so few
			// pixels that JPEG noise could pass.
			need = max(minRegionChange, min(need, regionPixels(cur, area)/8))
		}
		if n, _ := changed(before, cur, area); n > need {
			return fmt.Sprintf(" (changed after %d ms)", time.Since(start).Milliseconds()), true
		}
		if sleep(ctx, 80*time.Millisecond) != nil {
			break
		}
	}
	return fmt.Sprintf(" (timed out: no change within %d ms)", timeout.Milliseconds()), false
}

// fresh refuses a shot older than the sequence's max_age: what it showed may
// have moved even though the window did not.
func (r *runner) fresh(sh *shot) error {
	if r.maxAge > 0 && time.Since(sh.At) > r.maxAge {
		return fmt.Errorf("shot %d is %s old, over max_age %d ms: take a new screenshot (stale observation)", sh.ID, age(sh), r.maxAge.Milliseconds())
	}
	return nil
}

func age(sh *shot) string {
	return fmt.Sprintf("%.1f s", time.Since(sh.At).Seconds())
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

func tookControl(err error) bool {
	return errors.Is(err, human.ErrUserTookControl) || (err != nil && strings.Contains(err.Error(), "user took control"))
}
