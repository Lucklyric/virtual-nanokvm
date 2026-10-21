package hid

import (
	"testing"
	"time"
)

type fakePoster struct {
	x, y   float64
	events []string
}

func (f *fakePoster) cursor() (float64, float64)            { return f.x, f.y }
func (f *fakePoster) clamp(x, y float64) (float64, float64) { return x, y }

func (f *fakePoster) mouse(x, y float64, dx, dy int, button int, down *bool, held int, clicks int) error {
	f.x, f.y = x, y
	switch {
	case down == nil && held < 0:
		f.events = append(f.events, "move")
	case down == nil:
		f.events = append(f.events, "drag")
	case *down:
		f.events = append(f.events, "down"+string(rune('0'+button))+"x"+string(rune('0'+clicks)))
	default:
		f.events = append(f.events, "up"+string(rune('0'+button))+"x"+string(rune('0'+clicks)))
	}
	return nil
}

func (f *fakePoster) scroll(v, h int) error {
	f.events = append(f.events, "scroll")
	return nil
}

func (f *fakePoster) key(code uint16, down bool, mods uint8) error {
	f.events = append(f.events, keyEvent("key", code, down, mods))
	return nil
}

func (f *fakePoster) modifier(code uint16, down bool, mods uint8) error {
	f.events = append(f.events, keyEvent("mod", code, down, mods))
	return nil
}

func keyEvent(kind string, code uint16, down bool, mods uint8) string {
	dir := "up"
	if down {
		dir = "down"
	}
	return kind + ":" + string(rune('A'+code%26)) + ":" + dir + ":" + string(rune('0'+mods))
}

func newTestCG() (*CGEvent, *fakePoster, *time.Time) {
	p := &fakePoster{x: 100, y: 100}
	c := newCGEvent(p)
	clock := time.Unix(0, 0)
	c.now = func() time.Time { return clock }
	return c, p, &clock
}

func TestCGEventRelativeMoveAndDrag(t *testing.T) {
	c, p, _ := newTestCG()
	c.Pointer(0, 5, -3, 0, 0)
	if p.x != 105 || p.y != 97 {
		t.Fatalf("cursor at (%v, %v), want (105, 97)", p.x, p.y)
	}
	c.Pointer(ButtonLeft, 0, 0, 0, 0)
	c.Pointer(ButtonLeft, 2, 2, 0, 0)
	c.Pointer(0, 0, 0, 0, 0)
	want := []string{"move", "down0x1", "drag", "up0x1"}
	for i, w := range want {
		if p.events[i] != w {
			t.Fatalf("events = %v, want %v", p.events, want)
		}
	}
}

func TestCGEventDoubleClickCount(t *testing.T) {
	c, p, clock := newTestCG()
	click := func() {
		c.Pointer(ButtonLeft, 0, 0, 0, 0)
		*clock = clock.Add(80 * time.Millisecond)
		c.Pointer(0, 0, 0, 0, 0)
		*clock = clock.Add(100 * time.Millisecond)
	}
	click()
	click()
	*clock = clock.Add(time.Second)
	click()
	want := []string{"down0x1", "up0x1", "down0x2", "up0x2", "down0x1", "up0x1"}
	for i, w := range want {
		if p.events[i] != w {
			t.Fatalf("events = %v, want %v", p.events, want)
		}
	}
}

func TestCGEventKeyboardDiff(t *testing.T) {
	c, p, _ := newTestCG()
	// shift down, shift+h, shift (h up), all up
	c.Keyboard(LeftShift, nil)
	c.Keyboard(LeftShift, []uint16{0x0b})
	c.Keyboard(LeftShift, nil)
	c.Keyboard(0, nil)
	h := string(rune('A' + 0x04%26)) // macOS key code for h is 0x04
	want := []string{
		"mod:" + string(rune('A'+0x38%26)) + ":down:2",
		"key:" + h + ":down:2",
		"key:" + h + ":up:2",
		"mod:" + string(rune('A'+0x38%26)) + ":up:0",
	}
	if len(p.events) != len(want) {
		t.Fatalf("events = %v, want %v", p.events, want)
	}
	for i, w := range want {
		if p.events[i] != w {
			t.Fatalf("events = %v, want %v", p.events, want)
		}
	}
}

func TestEveryKeymapUsageHasKeycode(t *testing.T) {
	for u := uint16(0x04); u <= 0x52; u++ {
		if u == 0x32 || (u >= 0x46 && u <= 0x49) {
			continue // non-US hash key, print screen, scroll lock, pause, insert
		}
		if _, err := keycode(u); err != nil {
			t.Error(err)
		}
	}
}
