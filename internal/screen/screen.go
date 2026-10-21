// Package screen reads the cursor position and captures the main display.
//
// Positions are global display points (the coordinate space of the mouse).
package screen

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>

static CGPoint vnkvm_cursor(void) {
	CGEventRef e = CGEventCreate(NULL);
	CGPoint p = CGEventGetLocation(e);
	CFRelease(e);
	return p;
}
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Lucklyric/virtual-nanokvm/internal/human"
)

// Cursor implements human.Cursor with the live macOS cursor.
type Cursor struct{}

func (Cursor) Position() (human.Point, error) {
	p := C.vnkvm_cursor()
	return human.Point{X: float64(p.x), Y: float64(p.y)}, nil
}

// MainBounds returns the main display's size in points. The main display's
// origin is always (0, 0).
func MainBounds() (width, height float64) {
	b := C.CGDisplayBounds(C.CGMainDisplayID())
	return float64(b.size.width), float64(b.size.height)
}

// Screenshot captures the main display as a JPEG resized to width pixels.
// It needs Screen Recording permission for the app that launched this process.
func Screenshot(ctx context.Context, width int) ([]byte, error) {
	dir, err := os.MkdirTemp("", "vnkvm-shot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	raw := filepath.Join(dir, "raw.png")
	out := filepath.Join(dir, "shot.jpg")

	if b, err := exec.CommandContext(ctx, "screencapture", "-x", "-m", "-t", "png", raw).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("screencapture: %v: %s", err, b)
	}
	if _, err := os.Stat(raw); err != nil {
		return nil, fmt.Errorf("screencapture wrote no image (Screen Recording permission missing?)")
	}
	if b, err := exec.CommandContext(ctx, "sips",
		"--resampleWidth", fmt.Sprint(width),
		"-s", "format", "jpeg", "-s", "formatOptions", "75",
		raw, "--out", out).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sips: %v: %s", err, b)
	}
	return os.ReadFile(out)
}
