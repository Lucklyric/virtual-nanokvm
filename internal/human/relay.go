package human

import (
	"math"
	"time"

	"github.com/Lucklyric/virtual-nanokvm/internal/hid"
)

// Relay methods pass a remote person's own input through as-is: their hand
// already moves like a person, so nothing is generated. They share the
// engine's lock and button state with the agent actions, so the two never
// interleave reports.

// relayStale is how long after the last relayed move the engine trusts the
// real cursor again instead of its own record of where it sent it.
const relayStale = 300 * time.Millisecond

// RelayMove moves the cursor toward p, a point the remote hand is at now.
// Relative devices lag and accelerate, so it feeds forward from the previous
// relayed point and only gently corrects toward the real position.
func (e *Engine) RelayMove(p Point) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gain == 0 {
		e.gain = 1
	}
	actual, err := e.Cursor.Position()
	if err != nil {
		return err
	}
	fresh := e.relayAt.After(time.Now().Add(-relayStale))
	if fresh {
		// Learn the acceleration gain from how far the last report moved it.
		if sent := math.Hypot(float64(e.relaySentX), float64(e.relaySentY)); sent >= 3 {
			moved := math.Hypot(actual.X-e.relayFrom.X, actual.Y-e.relayFrom.Y)
			e.gain = 0.85*e.gain + 0.15*math.Max(0.2, math.Min(5, moved/sent))
		}
	}
	var wantX, wantY float64
	if fresh && p != e.relayLast {
		// The hand is moving: feed forward from the last relayed point, with
		// a gentle pull toward the real cursor (it lags a report behind).
		wantX = (p.X - e.relayLast.X + 0.3*(e.relayLast.X-actual.X)) / e.gain
		wantY = (p.Y - e.relayLast.Y + 0.3*(e.relayLast.Y-actual.Y)) / e.gain
	} else {
		// The hand holds still, or this is a new gesture: close half the gap
		// each time so acceleration cannot make it overshoot.
		wantX = float64(corrective(p.X-actual.X, e.gain))
		wantY = float64(corrective(p.Y-actual.Y, e.gain))
	}
	e.relayLast, e.relayAt, e.relayFrom = p, time.Now(), actual
	steps := max(1, int(math.Ceil(math.Max(math.Abs(wantX), math.Abs(wantY))/hid.MaxDelta)))
	sentX, sentY := 0, 0
	for i := 1; i <= steps; i++ {
		x := int(math.Round(wantX*float64(i)/float64(steps))) - sentX
		y := int(math.Round(wantY*float64(i)/float64(steps))) - sentY
		if x == 0 && y == 0 {
			continue
		}
		if err := e.HID.Pointer(e.buttons, x, y, 0, 0); err != nil {
			return err
		}
		sentX, sentY = sentX+x, sentY+y
	}
	e.relaySentX, e.relaySentY = sentX, sentY
	return nil
}

// RelayButtons sets which mouse buttons the remote hand holds.
func (e *Engine) RelayButtons(buttons uint32) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.buttons = buttons
	return e.HID.Pointer(e.buttons, 0, 0, 0, 0)
}

// RelayWheel turns the wheel. Positive dy scrolls down, positive dx right.
func (e *Engine) RelayWheel(dx, dy int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for dx != 0 || dy != 0 {
		if err := e.HID.Pointer(e.buttons, 0, 0, -sign(dy), sign(dx)); err != nil {
			return err
		}
		dy -= sign(dy)
		dx -= sign(dx)
	}
	return nil
}

// RelayKeys sets the full keyboard state the remote keyboard holds.
func (e *Engine) RelayKeys(modifiers uint8, usages []uint16) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.HID.Keyboard(modifiers, usages)
}

// ReleaseAll lets go of every key and button.
func (e *Engine) ReleaseAll() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.buttons = 0
	return e.HID.ReleaseAll()
}
