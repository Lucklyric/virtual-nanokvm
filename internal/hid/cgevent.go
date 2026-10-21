package hid

import (
	"fmt"
	"math"
	"time"
)

// Mouse buttons as CGMouseButton numbers.
const (
	cgLeft   = 0
	cgRight  = 1
	cgMiddle = 2
)

// eventPoster is the slice of Quartz Event Services the CGEvent backend uses.
// Positions are global display points.
type eventPoster interface {
	cursor() (x, y float64)
	clamp(x, y float64) (float64, float64)
	// mouse posts a move/drag (down=nil) or a button event at (x, y).
	mouse(x, y float64, dx, dy int, button int, down *bool, held int, clickCount int) error
	scroll(vertical, horizontal int) error
	key(keycode uint16, down bool, modifiers uint8) error
	modifier(keycode uint16, down bool, modifiers uint8) error
}

// CGEvent is a Backend that turns HID reports into synthetic Quartz events, the
// way RustDesk injects input. It needs no driver or root, only the
// Accessibility permission, but apps can tell the events are synthetic.
//
// It keeps the last report so each new one becomes the matching key, button,
// move and wheel events.
type CGEvent struct {
	post    eventPoster
	now     func() time.Time
	buttons uint32
	mods    uint8
	keys    map[uint16]bool

	// Synthetic events carry their own click count; macOS does not infer
	// double clicks from timing, so the backend must.
	lastDown      time.Time
	lastButton    int
	lastX, lastY  float64
	clickCount    int
	doubleClickIn time.Duration
}

func newCGEvent(p eventPoster) *CGEvent {
	return &CGEvent{post: p, now: time.Now, keys: map[uint16]bool{}, doubleClickIn: 500 * time.Millisecond}
}

var cgButtons = []struct {
	bit uint32
	cg  int
}{{ButtonLeft, cgLeft}, {ButtonRight, cgRight}, {ButtonMiddle, cgMiddle}}

// heldButton is the CGMouseButton a drag reports, or -1 when none is held.
func heldButton(buttons uint32) int {
	for _, b := range cgButtons {
		if buttons&b.bit != 0 {
			return b.cg
		}
	}
	return -1
}

func (c *CGEvent) Pointer(buttons uint32, dx, dy, vwheel, hwheel int) error {
	x, y := c.post.cursor()
	if dx != 0 || dy != 0 {
		x, y = c.post.clamp(x+float64(dx), y+float64(dy))
		if err := c.post.mouse(x, y, dx, dy, heldButton(c.buttons), nil, heldButton(c.buttons), 0); err != nil {
			return err
		}
	}
	for _, b := range cgButtons {
		was, is := c.buttons&b.bit != 0, buttons&b.bit != 0
		if was == is {
			continue
		}
		if is {
			t := c.now()
			if b.cg == c.lastButton && t.Sub(c.lastDown) <= c.doubleClickIn &&
				math.Hypot(x-c.lastX, y-c.lastY) <= 4 {
				c.clickCount++
			} else {
				c.clickCount = 1
			}
			c.lastDown, c.lastButton, c.lastX, c.lastY = t, b.cg, x, y
		}
		down := is
		if err := c.post.mouse(x, y, 0, 0, b.cg, &down, -1, c.clickCount); err != nil {
			return err
		}
	}
	c.buttons = buttons
	if vwheel != 0 || hwheel != 0 {
		return c.post.scroll(vwheel, hwheel)
	}
	return nil
}

// Virtual key codes of the left modifier keys, in HID modifier bit order.
var modifierKeycodes = [4]uint16{0x3B, 0x38, 0x3A, 0x37} // control, shift, option, command

func (c *CGEvent) Keyboard(modifiers uint8, usages []uint16) error {
	// Modifiers change first, so a shifted key goes down with shift held.
	for bit, code := range modifierKeycodes {
		m := uint8(1) << bit
		if (c.mods&m != 0) == (modifiers&m != 0) {
			continue
		}
		next := c.mods ^ m
		if err := c.post.modifier(code, modifiers&m != 0, next); err != nil {
			return err
		}
		c.mods = next
	}
	want := map[uint16]bool{}
	for _, u := range usages {
		want[u] = true
	}
	for u := range c.keys {
		if !want[u] {
			code, err := keycode(u)
			if err != nil {
				return err
			}
			if err := c.post.key(code, false, c.mods); err != nil {
				return err
			}
			delete(c.keys, u)
		}
	}
	for _, u := range usages {
		if c.keys[u] {
			continue
		}
		code, err := keycode(u)
		if err != nil {
			return err
		}
		if err := c.post.key(code, true, c.mods); err != nil {
			return err
		}
		c.keys[u] = true
	}
	return nil
}

func (c *CGEvent) ReleaseAll() error {
	if err := c.Keyboard(0, nil); err != nil {
		return err
	}
	return c.Pointer(0, 0, 0, 0, 0)
}

func (c *CGEvent) Close() error { return nil }

// keycode maps a HID keyboard usage to a macOS virtual key code (ANSI layout).
func keycode(usage uint16) (uint16, error) {
	if code, ok := hidToKeycode[usage]; ok {
		return code, nil
	}
	return 0, fmt.Errorf("no macOS key code for HID usage %#x", usage)
}

var hidToKeycode = map[uint16]uint16{
	// a-z
	0x04: 0x00, 0x05: 0x0B, 0x06: 0x08, 0x07: 0x02, 0x08: 0x0E, 0x09: 0x03,
	0x0a: 0x05, 0x0b: 0x04, 0x0c: 0x22, 0x0d: 0x26, 0x0e: 0x28, 0x0f: 0x25,
	0x10: 0x2E, 0x11: 0x2D, 0x12: 0x1F, 0x13: 0x23, 0x14: 0x0C, 0x15: 0x0F,
	0x16: 0x01, 0x17: 0x11, 0x18: 0x20, 0x19: 0x09, 0x1a: 0x0D, 0x1b: 0x07,
	0x1c: 0x10, 0x1d: 0x06,
	// 1-9, 0
	0x1e: 0x12, 0x1f: 0x13, 0x20: 0x14, 0x21: 0x15, 0x22: 0x17,
	0x23: 0x16, 0x24: 0x1A, 0x25: 0x1C, 0x26: 0x19, 0x27: 0x1D,
	// enter, escape, backspace, tab, space, punctuation
	0x28: 0x24, 0x29: 0x35, 0x2a: 0x33, 0x2b: 0x30, 0x2c: 0x31,
	0x2d: 0x1B, 0x2e: 0x18, 0x2f: 0x21, 0x30: 0x1E, 0x31: 0x2A,
	0x33: 0x29, 0x34: 0x27, 0x35: 0x32, 0x36: 0x2B, 0x37: 0x2F, 0x38: 0x2C,
	0x39: 0x39, // caps lock
	// F1-F12
	0x3a: 0x7A, 0x3b: 0x78, 0x3c: 0x63, 0x3d: 0x76, 0x3e: 0x60, 0x3f: 0x61,
	0x40: 0x62, 0x41: 0x64, 0x42: 0x65, 0x43: 0x6D, 0x44: 0x67, 0x45: 0x6F,
	// home, page up, forward delete, end, page down, arrows
	0x4a: 0x73, 0x4b: 0x74, 0x4c: 0x75, 0x4d: 0x77, 0x4e: 0x79,
	0x4f: 0x7C, 0x50: 0x7B, 0x51: 0x7D, 0x52: 0x7E,
}
