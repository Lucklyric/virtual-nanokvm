// Package keymap maps text and key names to USB HID keyboard usages for a US
// ANSI layout (the layout the virtual keyboard announces).
package keymap

import (
	"fmt"
	"strings"

	"github.com/Lucklyric/virtual-nanokvm/internal/hid"
)

// Stroke is one key press: a usage plus the modifiers held with it.
type Stroke struct {
	Modifiers uint8
	Usage     uint16
}

var unshifted = map[rune]uint16{
	'1': 0x1e, '2': 0x1f, '3': 0x20, '4': 0x21, '5': 0x22,
	'6': 0x23, '7': 0x24, '8': 0x25, '9': 0x26, '0': 0x27,
	'\n': 0x28, '\t': 0x2b, ' ': 0x2c,
	'-': 0x2d, '=': 0x2e, '[': 0x2f, ']': 0x30, '\\': 0x31,
	';': 0x33, '\'': 0x34, '`': 0x35, ',': 0x36, '.': 0x37, '/': 0x38,
}

var shifted = map[rune]uint16{
	'!': 0x1e, '@': 0x1f, '#': 0x20, '$': 0x21, '%': 0x22,
	'^': 0x23, '&': 0x24, '*': 0x25, '(': 0x26, ')': 0x27,
	'_': 0x2d, '+': 0x2e, '{': 0x2f, '}': 0x30, '|': 0x31,
	':': 0x33, '"': 0x34, '~': 0x35, '<': 0x36, '>': 0x37, '?': 0x38,
}

var named = map[string]uint16{
	"enter": 0x28, "return": 0x28, "escape": 0x29, "esc": 0x29,
	"backspace": 0x2a, "tab": 0x2b, "space": 0x2c, "capslock": 0x39,
	"f1": 0x3a, "f2": 0x3b, "f3": 0x3c, "f4": 0x3d, "f5": 0x3e, "f6": 0x3f,
	"f7": 0x40, "f8": 0x41, "f9": 0x42, "f10": 0x43, "f11": 0x44, "f12": 0x45,
	"home": 0x4a, "pageup": 0x4b, "delete": 0x4c, "end": 0x4d, "pagedown": 0x4e,
	"right": 0x4f, "left": 0x50, "down": 0x51, "up": 0x52,
}

var modifierNames = map[string]uint8{
	"ctrl": hid.LeftControl, "control": hid.LeftControl,
	"shift": hid.LeftShift,
	"alt":   hid.LeftOption, "opt": hid.LeftOption, "option": hid.LeftOption,
	"cmd": hid.LeftCommand, "command": hid.LeftCommand, "meta": hid.LeftCommand,
}

// Char returns the stroke that types r.
func Char(r rune) (Stroke, error) {
	switch {
	case r >= 'a' && r <= 'z':
		return Stroke{Usage: 0x04 + uint16(r-'a')}, nil
	case r >= 'A' && r <= 'Z':
		return Stroke{Modifiers: hid.LeftShift, Usage: 0x04 + uint16(r-'A')}, nil
	}
	if u, ok := unshifted[r]; ok {
		return Stroke{Usage: u}, nil
	}
	if u, ok := shifted[r]; ok {
		return Stroke{Modifiers: hid.LeftShift, Usage: u}, nil
	}
	return Stroke{}, fmt.Errorf("character %q has no key on a US keyboard", r)
}

// Combo parses a key combination such as "cmd+shift+4", "enter" or "ctrl+a".
// It returns the modifiers in the order given (so they can be pressed in that
// order) and the final key.
func Combo(spec string) ([]uint8, Stroke, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(spec)), "+")
	var mods []uint8
	var all uint8
	for _, p := range parts[:len(parts)-1] {
		m, ok := modifierNames[strings.TrimSpace(p)]
		if !ok {
			return nil, Stroke{}, fmt.Errorf("unknown modifier %q in %q", p, spec)
		}
		mods = append(mods, m)
		all |= m
	}
	key := strings.TrimSpace(parts[len(parts)-1])
	if u, ok := named[key]; ok {
		return mods, Stroke{Modifiers: all, Usage: u}, nil
	}
	if r := []rune(key); len(r) == 1 {
		s, err := Char(r[0])
		if err != nil {
			return nil, Stroke{}, err
		}
		s.Modifiers |= all
		return mods, s, nil
	}
	return nil, Stroke{}, fmt.Errorf("unknown key %q in %q", key, spec)
}

// Modifier bits for browser KeyboardEvent.code values of modifier keys.
var codeModifiers = map[string]uint8{
	"ControlLeft": hid.LeftControl, "ControlRight": hid.LeftControl,
	"ShiftLeft": hid.LeftShift, "ShiftRight": hid.LeftShift,
	"AltLeft": hid.LeftOption, "AltRight": hid.LeftOption,
	"MetaLeft": hid.LeftCommand, "MetaRight": hid.LeftCommand,
}

var codeUsages = map[string]uint16{
	"Enter": 0x28, "Escape": 0x29, "Backspace": 0x2a, "Tab": 0x2b, "Space": 0x2c,
	"Minus": 0x2d, "Equal": 0x2e, "BracketLeft": 0x2f, "BracketRight": 0x30,
	"Backslash": 0x31, "Semicolon": 0x33, "Quote": 0x34, "Backquote": 0x35,
	"Comma": 0x36, "Period": 0x37, "Slash": 0x38, "CapsLock": 0x39,
	"Home": 0x4a, "PageUp": 0x4b, "Delete": 0x4c, "End": 0x4d, "PageDown": 0x4e,
	"ArrowRight": 0x4f, "ArrowLeft": 0x50, "ArrowDown": 0x51, "ArrowUp": 0x52,
}

// Code maps a browser KeyboardEvent.code (a physical key, independent of
// layout) to a HID usage, or to a modifier bit for modifier keys.
func Code(code string) (usage uint16, modifier uint8, ok bool) {
	if m, ok := codeModifiers[code]; ok {
		return 0, m, true
	}
	if u, ok := codeUsages[code]; ok {
		return u, 0, true
	}
	if len(code) == 4 && code[:3] == "Key" && code[3] >= 'A' && code[3] <= 'Z' {
		return 0x04 + uint16(code[3]-'A'), 0, true
	}
	if len(code) == 6 && code[:5] == "Digit" && code[5] >= '0' && code[5] <= '9' {
		if code[5] == '0' {
			return 0x27, 0, true
		}
		return 0x1e + uint16(code[5]-'1'), 0, true
	}
	if len(code) >= 2 && code[0] == 'F' {
		var n int
		if _, err := fmt.Sscanf(code[1:], "%d", &n); err == nil && n >= 1 && n <= 12 {
			return 0x3a + uint16(n-1), 0, true
		}
	}
	return 0, 0, false
}
