package screen

import (
	"context"
	"image"
	"os"
	"strconv"
	"testing"
	"time"
)

// VNKVM_LIVE=1 VNKVM_WINDOW=<id> go test ./internal/screen -run CaptureLive -v
// needs Screen Recording permission for the terminal. Without VNKVM_WINDOW it
// captures the frontmost app window.
func TestCaptureLive(t *testing.T) {
	if os.Getenv("VNKVM_LIVE") == "" {
		t.Skip("set VNKVM_LIVE=1 to capture the real screen")
	}
	var id uint32
	if v := os.Getenv("VNKVM_WINDOW"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		id = uint32(n)
	} else {
		ws := Windows()
		if len(ws) == 0 {
			t.Fatal("no window to capture")
		}
		id = ws[0].ID
	}

	var fast *image.RGBA
	for i := range 3 {
		start := time.Now()
		img, err := captureSCK(id)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("in process #%d: %v %v", i, time.Since(start).Round(time.Millisecond), img.Bounds().Size())
		fast = img
	}
	start := time.Now()
	slow, err := captureCommand(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("screencapture: %v %v", time.Since(start).Round(time.Millisecond), slow.Bounds().Size())

	if fast.Bounds().Size() != slow.Bounds().Size() {
		t.Fatalf("size differs: in process %v, screencapture %v", fast.Bounds().Size(), slow.Bounds().Size())
	}
	// Mean channel difference over a sparse grid; the window may change between
	// the two captures, so only a large difference (color space, layout) fails.
	b := fast.Bounds()
	var sum, n float64
	for y := b.Min.Y; y < b.Max.Y; y += 7 {
		for x := b.Min.X; x < b.Max.X; x += 7 {
			r1, g1, b1, _ := fast.At(x, y).RGBA()
			r2, g2, b2, _ := slow.At(x, y).RGBA()
			sum += absDiff(r1, r2) + absDiff(g1, g2) + absDiff(b1, b2)
			n += 3
		}
	}
	mean := sum / n / 257
	t.Logf("mean channel difference: %.2f of 255", mean)
	if mean > 12 {
		t.Fatalf("captures differ too much: %.2f", mean)
	}
}

func absDiff(a, b uint32) float64 {
	if a > b {
		return float64(a - b)
	}
	return float64(b - a)
}
