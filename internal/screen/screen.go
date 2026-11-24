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
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

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
// permission for the app that launched this process. It captures in process
// and falls back to screencapture when that fails. The first fallback is
// logged with its reason, since that path is several times slower and
// nothing else shows which one is in use.
func Capture(ctx context.Context, window uint32) (image.Image, error) {
	img, err := captureSCK(window)
	if err == nil {
		return img, nil
	}
	fellBack.Do(func() {
		log.Printf("screen: in-process capture failed (%v); using the screencapture command instead, which is slower. vnkvm doctor shows the capture path.", err)
	})
	return captureCommand(ctx, window)
}

var fellBack sync.Once

// CapturePath reports which path Capture uses in this process right now, by
// capturing the main display in process. When that fails, reason says why and
// Capture runs the screencapture command.
func CapturePath() (inProcess bool, reason string) {
	if _, err := captureSCK(0); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// captureCommand captures with the screencapture command.
func captureCommand(ctx context.Context, window uint32) (image.Image, error) {
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
