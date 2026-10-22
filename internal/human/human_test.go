package human

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/Lucklyric/virtual-nanokvm/internal/hid"
)

// simScreen imitates macOS handling a relative mouse: speed-dependent pointer
// acceleration, clamping at the screen edges, and a one-tick delay before a
// report shows up in the reported cursor position.
type simScreen struct {
	visible, pending Point
	w, h             float64
	accel            float64 // extra gain per unit of report speed
	elapsed          time.Duration
}

func (s *simScreen) Position() (Point, error) { return s.visible, nil }

func (s *simScreen) apply(dx, dy int) {
	accel := func(d int) float64 { return float64(d) * (0.8 + s.accel*math.Abs(float64(d))) }
	s.pending.X = math.Max(0, math.Min(s.w-1, s.pending.X+accel(dx)))
	s.pending.Y = math.Max(0, math.Min(s.h-1, s.pending.Y+accel(dy)))
}

func (s *simScreen) sleep(_ context.Context, d time.Duration) error {
	s.elapsed += d
	s.visible = s.pending
	return nil
}

func newSim(seed uint64, start Point) (*Engine, *hid.Recorder, *simScreen) {
	scr := &simScreen{visible: start, pending: start, w: 1512, h: 982, accel: 0.05}
	rec := &hid.Recorder{OnPointer: scr.apply}
	e := &Engine{
		HID:    rec,
		Cursor: scr,
		Rand:   rand.New(rand.NewPCG(seed, seed)),
		Sleep:  scr.sleep,
		Tick:   8 * time.Millisecond,
	}
	return e, rec, scr
}

func TestMoveToReachesTarget(t *testing.T) {
	targets := []Point{{1200, 700}, {50, 40}, {760, 480}, {761, 482}, {1500, 970}}
	for seed := uint64(1); seed <= 40; seed++ {
		e, _, scr := newSim(seed, Point{100, 100})
		// Cover gentle, default and very aggressive acceleration curves.
		scr.accel = []float64{0, 0.05, 0.3}[seed%3]
		for _, target := range targets {
			got, err := e.MoveTo(context.Background(), target, 20)
			if err != nil {
				t.Fatalf("seed %d target %v: %v", seed, target, err)
			}
			if math.Abs(got.X-target.X) >= 1 || math.Abs(got.Y-target.Y) >= 1 {
				t.Fatalf("seed %d: ended at %v, want %v", seed, got, target)
			}
			if scr.visible != got {
				t.Fatalf("seed %d: returned %v but cursor is at %v", seed, got, scr.visible)
			}
		}
	}
}

func TestMoveIsHumanLike(t *testing.T) {
	e, rec, scr := newSim(7, Point{100, 100})
	if _, err := e.MoveTo(context.Background(), Point{1300, 800}, 20); err != nil {
		t.Fatal(err)
	}

	// A 1400-point move to a 20-point target should take most of a second, not
	// an instant jump.
	if scr.elapsed < 500*time.Millisecond || scr.elapsed > 2*time.Second {
		t.Fatalf("move took %v", scr.elapsed)
	}
	if len(rec.Reports) < 40 {
		t.Fatalf("only %d reports; expected a stream of small steps", len(rec.Reports))
	}

	// Speed should rise then fall: the middle third of the stroke carries more
	// motion per report than the first and last sixths.
	n := len(rec.Reports)
	step := func(r hid.Report) float64 { return math.Hypot(float64(r.DX), float64(r.DY)) }
	avg := func(rs []hid.Report) float64 {
		sum := 0.0
		for _, r := range rs {
			sum += step(r)
		}
		return sum / float64(len(rs))
	}
	start, mid, end := avg(rec.Reports[:n/6]), avg(rec.Reports[n/3:2*n/3]), avg(rec.Reports[5*n/6:])
	if mid <= start || mid <= end {
		t.Fatalf("speed profile not bell-shaped: start %.1f mid %.1f end %.1f", start, mid, end)
	}
}

func TestPathIsCurved(t *testing.T) {
	// Track the cursor between reports and measure how far the path strays
	// from the straight line; a person never moves on a ruler.
	maxDev := 0.0
	for seed := uint64(1); seed <= 10; seed++ {
		from, to := Point{100, 500}, Point{1400, 500}
		e, rec, scr := newSim(seed, from)
		rec.OnPointer = func(dx, dy int) {
			scr.apply(dx, dy)
			maxDev = math.Max(maxDev, math.Abs(scr.pending.Y-from.Y))
		}
		if _, err := e.MoveTo(context.Background(), to, 20); err != nil {
			t.Fatal(err)
		}
	}
	if maxDev < 20 {
		t.Fatalf("paths stayed within %.1f points of a straight line", maxDev)
	}
}

func TestStuckAtEdgeReportsError(t *testing.T) {
	e, _, _ := newSim(3, Point{100, 100})
	if _, err := e.MoveTo(context.Background(), Point{2000, 100}, 20); err == nil {
		t.Fatal("expected an error for an off-screen target")
	}
}

func TestClickAndType(t *testing.T) {
	e, rec, _ := newSim(1, Point{0, 0})
	ctx := context.Background()
	if err := e.Click(ctx, Left, 2); err != nil {
		t.Fatal(err)
	}
	want := []uint32{1, 0, 1, 0}
	for i, b := range want {
		if rec.Reports[i].Buttons != b {
			t.Fatalf("report %d buttons = %d, want %d", i, rec.Reports[i].Buttons, b)
		}
	}

	rec.Reports = nil
	if err := e.Type(ctx, "Hi", 70); err != nil {
		t.Fatal(err)
	}
	// H: shift down, shift+h, shift (h up), all up. i: i down, all up.
	got := rec.Reports
	if len(got) != 6 || got[0].Modifiers != hid.LeftShift || got[1].Usages[0] != 0x0b ||
		got[3].Modifiers != 0 || got[4].Usages[0] != 0x0c || len(got[5].Usages) != 0 {
		t.Fatalf("unexpected keyboard reports: %+v", got)
	}

	rec.Reports = nil
	if err := e.Type(ctx, "café", 70); err == nil || len(rec.Reports) != 0 {
		t.Fatal("expected unsupported character to fail before any key is pressed")
	}
}

func TestPressCombo(t *testing.T) {
	e, rec, _ := newSim(1, Point{0, 0})
	if err := e.Press(context.Background(), "cmd+shift+4"); err != nil {
		t.Fatal(err)
	}
	mods := []uint8{
		hid.LeftCommand,
		hid.LeftCommand | hid.LeftShift,
		hid.LeftCommand | hid.LeftShift, // + key 4
		hid.LeftCommand | hid.LeftShift, // key up
		hid.LeftCommand,
		0,
	}
	if len(rec.Reports) != len(mods) {
		t.Fatalf("got %d reports: %+v", len(rec.Reports), rec.Reports)
	}
	for i, m := range mods {
		if rec.Reports[i].Modifiers != m {
			t.Fatalf("report %d modifiers = %08b, want %08b", i, rec.Reports[i].Modifiers, m)
		}
	}
	if rec.Reports[2].Usages[0] != 0x21 {
		t.Fatalf("key usage = %#x, want 0x21", rec.Reports[2].Usages[0])
	}
}

func TestEveryMoveIsDifferent(t *testing.T) {
	// Same start, same target, many moves: duration, path and report stream
	// must vary from move to move, never replaying one fixed trajectory.
	e, rec, scr := newSim(42, Point{200, 200})
	ctx := context.Background()
	durations := map[time.Duration]bool{}
	paths := map[string]bool{}
	for range 20 {
		scr.visible, scr.pending, scr.elapsed = Point{200, 200}, Point{200, 200}, 0
		rec.Reports = nil
		if _, err := e.MoveTo(ctx, Point{1100, 700}, 20); err != nil {
			t.Fatal(err)
		}
		durations[scr.elapsed] = true
		sig := ""
		for _, r := range rec.Reports[:10] {
			sig += fmt.Sprint(r.DX, r.DY, ";")
		}
		paths[sig] = true
	}
	if len(durations) < 15 || len(paths) < 18 {
		t.Fatalf("moves too repetitive: %d distinct durations, %d distinct paths of 20", len(durations), len(paths))
	}
}

// grab simulates a hand on the mouse: after `after` sleeps it shoves the
// cursor by (dx, dy).
func grab(scr *simScreen, e *Engine, after int, dx, dy float64) {
	n := 0
	e.Sleep = func(ctx context.Context, d time.Duration) error {
		if n++; n == after {
			scr.pending.X += dx
			scr.pending.Y += dy
		}
		return scr.sleep(ctx, d)
	}
}

func assertReleased(t *testing.T, rec *hid.Recorder) {
	t.Helper()
	n := len(rec.Reports)
	if n < 2 {
		t.Fatal("no release reports")
	}
	kb, ptr := rec.Reports[n-2], rec.Reports[n-1]
	if !kb.Keyboard || kb.Modifiers != 0 || len(kb.Usages) != 0 || ptr.Keyboard || ptr.Buttons != 0 {
		t.Fatalf("last reports are not a full release: %+v %+v", kb, ptr)
	}
}

func TestTakeoverDuringMove(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		e, rec, scr := newSim(seed, Point{100, 100})
		grab(scr, e, 20, -150, 220)
		_, err := e.MoveTo(context.Background(), Point{1300, 800}, 20)
		if !errors.Is(err, ErrUserTookControl) {
			t.Fatalf("seed %d: err = %v, want ErrUserTookControl", seed, err)
		}
		assertReleased(t, rec)
	}
}

func TestTakeoverDuringDrag(t *testing.T) {
	e, rec, scr := newSim(5, Point{100, 100})
	grab(scr, e, 30, 0, -200)
	if _, err := e.Drag(context.Background(), Left, Point{1000, 700}); !errors.Is(err, ErrUserTookControl) {
		t.Fatalf("err = %v, want ErrUserTookControl", err)
	}
	assertReleased(t, rec)
}

func TestTakeoverDuringTyping(t *testing.T) {
	e, rec, scr := newSim(1, Point{500, 500})
	grab(scr, e, 7, 30, 0)
	err := e.Type(context.Background(), "a long sentence to type", 70)
	if !errors.Is(err, ErrUserTookControl) {
		t.Fatalf("err = %v, want ErrUserTookControl", err)
	}
	assertReleased(t, rec)
	// It stopped early instead of typing every character.
	downs := 0
	for _, r := range rec.Reports {
		if r.Keyboard && len(r.Usages) > 0 {
			downs++
		}
	}
	if downs >= len("a long sentence to type") {
		t.Fatalf("typed %d keys after the takeover", downs)
	}
}

func TestSmallTremorIsNotTakeover(t *testing.T) {
	// A 2-point nudge while typing stays under the idle tolerance.
	e, _, scr := newSim(1, Point{500, 500})
	grab(scr, e, 5, 2, 0)
	if err := e.Type(context.Background(), "hello", 70); err != nil {
		t.Fatal(err)
	}
}

func TestClickBehaviorVaries(t *testing.T) {
	// Across many clicks: the press always lands on the spot, while the
	// hover wobble and the post-click drift show up only some of the time.
	wobbles, drifts := 0, 0
	for seed := uint64(1); seed <= 60; seed++ {
		e, rec, scr := newSim(seed, Point{400, 300})
		if err := e.Click(context.Background(), Left, 1); err != nil {
			t.Fatal(err)
		}
		pressAt := -1
		for i, r := range rec.Reports {
			if r.Buttons != 0 {
				pressAt = i
				break
			}
		}
		if pressAt > 0 {
			wobbles++
		}
		if scr.visible != (Point{400, 300}) && math.Hypot(scr.pending.X-400, scr.pending.Y-300) >= 2 {
			drifts++
		}
		// Replay up to the press to find where it happened.
		x, y := 400.0, 300.0
		for _, r := range rec.Reports[:pressAt] {
			x += float64(r.DX) * (0.8 + scr.accel*math.Abs(float64(r.DX)))
			y += float64(r.DY) * (0.8 + scr.accel*math.Abs(float64(r.DY)))
		}
		if math.Hypot(x-400, y-300) >= 1 {
			t.Fatalf("seed %d: pressed at (%.1f, %.1f), not on the spot", seed, x, y)
		}
	}
	if wobbles == 0 || wobbles == 60 || drifts == 0 || drifts == 60 {
		t.Fatalf("behavior not random: %d/60 wobbles, %d/60 drifts", wobbles, drifts)
	}
}

func TestRelayFollowsRemoteHand(t *testing.T) {
	// A remote hand's path relayed event by event: the cursor tracks it
	// closely despite acceleration and the one-tick lag.
	for _, accel := range []float64{0, 0.05, 0.3} {
		e, _, scr := newSim(1, Point{100, 100})
		scr.accel = accel
		worst := 0.0
		for i := 1; i <= 60; i++ {
			p := Point{100 + float64(i)*15, 100 + float64(i)*6}
			if err := e.RelayMove(p); err != nil {
				t.Fatal(err)
			}
			scr.sleep(context.Background(), 16*time.Millisecond)
			if i > 3 {
				worst = math.Max(worst, math.Hypot(scr.visible.X-p.X, scr.visible.Y-p.Y))
			}
		}
		// Hold still: the cursor converges onto the last point.
		last := Point{1000, 460}
		for range 10 {
			e.RelayMove(last)
			scr.sleep(context.Background(), 16*time.Millisecond)
		}
		if d := math.Hypot(scr.visible.X-last.X, scr.visible.Y-last.Y); d > 1.5 || worst > 40 {
			t.Fatalf("accel %.2f: final error %.1f, worst lag %.1f", accel, d, worst)
		}
	}
}

func TestSpeedScalesMovesAndClicks(t *testing.T) {
	timed := func(speed float64) time.Duration {
		var total time.Duration
		for seed := uint64(1); seed <= 20; seed++ {
			e, _, scr := newSim(seed, Point{100, 100})
			if speed != 1 {
				if _, err := e.SetSpeed(speed); err != nil {
					t.Fatal(err)
				}
			}
			got, err := e.MoveTo(context.Background(), Point{1300, 800}, 20)
			if err != nil || math.Abs(got.X-1300) >= 1 || math.Abs(got.Y-800) >= 1 {
				t.Fatalf("speed %v seed %d: ended at %v, %v", speed, seed, got, err)
			}
			if err := e.Click(context.Background(), Left, 1); err != nil {
				t.Fatal(err)
			}
			total += scr.elapsed
		}
		return total / 20
	}
	normal, fast, fastest := timed(1), timed(2), timed(MaxSpeed)
	t.Logf("1400 pt move and click: speed 1 %v, 2 %v, %v %v", normal, fast, MaxSpeed, fastest)
	if fast > normal*7/10 || fastest > normal*4/10 {
		t.Fatalf("move and click: normal %v, speed 2 %v, speed %v %v", normal, fast, MaxSpeed, fastest)
	}
	e, _, _ := newSim(1, Point{})
	for _, bad := range []float64{0, 0.1, 6} {
		if _, err := e.SetSpeed(bad); err == nil {
			t.Fatalf("speed %v accepted", bad)
		}
	}
}
