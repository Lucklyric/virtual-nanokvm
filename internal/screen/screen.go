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
	"image"
	"image/png"
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

// Capture grabs the main display (window 0) or one window, even when other
// windows cover it, at full resolution. It needs the Screen Recording
// permission for the app that launched this process.
func Capture(ctx context.Context, window uint32) (image.Image, error) {
	dir, err := os.MkdirTemp("", "vnkvm-shot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	raw := filepath.Join(dir, "raw.png")
	args := []string{"-x", "-m", "-t", "png", raw}
	if window != 0 {
		args = []string{"-x", "-o", "-l", fmt.Sprint(window), "-t", "png", raw}
	}
	if b, err := exec.CommandContext(ctx, "screencapture", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("screencapture: %v: %s", err, b)
	}
	f, err := os.Open(raw)
	if err != nil {
		return nil, fmt.Errorf("screencapture wrote no image (Screen Recording permission missing?)")
	}
	defer f.Close()
	return png.Decode(f)
}
