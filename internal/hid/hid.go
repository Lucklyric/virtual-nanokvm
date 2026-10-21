// Package hid sends raw USB HID reports to the virtual keyboard and mouse.
//
// The real backend talks to vnkvm-hid-bridge, a root helper that owns the
// Karabiner-DriverKit-VirtualHIDDevice connection. macOS sees those reports as
// a physical USB keyboard and mouse.
package hid

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Modifier bits of a boot keyboard report.
const (
	LeftControl uint8 = 1 << 0
	LeftShift   uint8 = 1 << 1
	LeftOption  uint8 = 1 << 2
	LeftCommand uint8 = 1 << 3
)

// Mouse button bits of a pointing report.
const (
	ButtonLeft   uint32 = 1 << 0
	ButtonRight  uint32 = 1 << 1
	ButtonMiddle uint32 = 1 << 2
)

// MaxDelta is the largest movement or wheel value one pointing report carries.
const MaxDelta = 127

// Backend accepts HID reports. A keyboard report holds the full state of the
// keyboard (all keys currently down); an empty report releases everything.
type Backend interface {
	Keyboard(modifiers uint8, usages []uint16) error
	// Pointer sends one relative mouse report. Deltas must be in
	// [-MaxDelta, MaxDelta]; buttons is the set of buttons held down.
	Pointer(buttons uint32, dx, dy, vwheel, hwheel int) error
	ReleaseAll() error
	Close() error
}

// Bridge is the Backend backed by vnkvm-hid-bridge over a unix socket.
type Bridge struct {
	mu   sync.Mutex
	conn net.Conn
	r    *bufio.Reader
}

// DialBridge connects to the bridge and checks that both virtual devices are ready.
func DialBridge(socketPath string) (*Bridge, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to hid bridge at %s: %w", socketPath, err)
	}
	b := &Bridge{conn: conn, r: bufio.NewReader(conn)}
	// The bridge reaches the Karabiner daemon asynchronously, so a freshly
	// started bridge reports not-ready for a moment. Wait up to 5 s.
	var status string
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		status, err = b.call("S")
		if err != nil {
			conn.Close()
			return nil, err
		}
		if status == "ready keyboard=1 pointing=1" || time.Now().After(deadline) {
			break
		}
	}
	if status != "ready keyboard=1 pointing=1" {
		conn.Close()
		return nil, fmt.Errorf("virtual HID devices not ready (%s): is the Karabiner driver activated and its daemon running?", status)
	}
	return b, nil
}

func (b *Bridge) call(line string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.conn.Write([]byte(line + "\n")); err != nil {
		return "", fmt.Errorf("hid bridge write: %w", err)
	}
	reply, err := b.r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("hid bridge read: %w", err)
	}
	reply = strings.TrimSpace(reply)
	if strings.HasPrefix(reply, "err ") {
		return "", fmt.Errorf("hid bridge: %s", strings.TrimPrefix(reply, "err "))
	}
	return reply, nil
}

func (b *Bridge) Keyboard(modifiers uint8, usages []uint16) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "K %d", modifiers)
	for _, u := range usages {
		fmt.Fprintf(&sb, " %d", u)
	}
	_, err := b.call(sb.String())
	return err
}

func (b *Bridge) Pointer(buttons uint32, dx, dy, vwheel, hwheel int) error {
	_, err := b.call(fmt.Sprintf("P %d %d %d %d %d", buttons, dx, dy, vwheel, hwheel))
	return err
}

func (b *Bridge) ReleaseAll() error {
	_, err := b.call("R")
	return err
}

func (b *Bridge) Close() error {
	return b.conn.Close()
}
