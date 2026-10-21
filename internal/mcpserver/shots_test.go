package mcpserver

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"strings"
	"testing"

	"github.com/Lucklyric/virtual-nanokvm/internal/human"
	"github.com/Lucklyric/virtual-nanokvm/internal/screen"
)

// fakeScreen is a 1512×982-point Retina display (2 pixels per point) with
// windows that tests can move, resize and cover.
type fakeScreen struct {
	windows []screen.Window // frontmost first
}

func (f *fakeScreen) Bounds() (float64, float64) { return 1512, 982 }
func (f *fakeScreen) Windows() []screen.Window   { return f.windows }
func (f *fakeScreen) FindWindow(id uint32, app string) (screen.Window, error) {
	for _, w := range f.windows {
		if w.ID == id || (id == 0 && w.App == app) {
			return w, nil
		}
	}
	return screen.Window{}, fmt.Errorf("no window %d", id)
}
func (f *fakeScreen) WindowAt(x, y float64) (screen.Window, bool) {
	for _, w := range f.windows {
		if x >= w.X && y >= w.Y && x < w.X+w.Width && y < w.Y+w.Height {
			return w, true
		}
	}
	return screen.Window{}, false
}
func (f *fakeScreen) Capture(_ context.Context, id uint32) (image.Image, error) {
	w, h := 1512.0, 982.0
	if id != 0 {
		win, err := f.FindWindow(id, "")
		if err != nil {
			return nil, err
		}
		w, h = win.Width, win.Height
	}
	img := image.NewRGBA(image.Rect(0, 0, int(w*2), int(h*2)))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	return img, nil
}
func (f *fakeScreen) Preview(int) ([]byte, error) { return nil, fmt.Errorf("no preview") }

func near(t *testing.T, what string, got, want human.Point, tol float64) {
	t.Helper()
	if math.Abs(got.X-want.X) > tol || math.Abs(got.Y-want.Y) > tol {
		t.Fatalf("%s: got (%.2f, %.2f), want (%.2f, %.2f) ±%.2f", what, got.X, got.Y, want.X, want.Y, tol)
	}
}

func TestDisplayShotMapsPixelCenters(t *testing.T) {
	scr, reg := &fakeScreen{}, &shots{}
	sh, _, err := take(context.Background(), scr, reg, shotRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if sh.W != 1024 || sh.H != 665 {
		t.Fatalf("low shot is %dx%d, want 1024x665", sh.W, sh.H)
	}
	k := 1512.0 / 1024
	p, err := toGlobal(scr, sh, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "top-left pixel", p, human.Point{X: 0.5 * k, Y: 0.5 * k}, 0.01)
	p, _ = toGlobal(scr, sh, 1023, 664)
	if p.X >= 1512 || p.Y >= 982 {
		t.Fatalf("bottom-right pixel maps off the display: %v", p)
	}
	if _, err := toGlobal(scr, sh, 1024, 10); err == nil {
		t.Fatal("a pixel outside the image was accepted")
	}
}

func TestZoomRoundTrip(t *testing.T) {
	scr, reg := &fakeScreen{}, &shots{}
	ctx := context.Background()
	low, _, _ := take(ctx, scr, reg, shotRequest{})
	zoom, _, err := take(ctx, scr, reg, shotRequest{Base: low, Region: [4]float64{512, 256, 128, 64}})
	if err != nil {
		t.Fatal(err)
	}
	if zoom.ID == low.ID {
		t.Fatal("the zoom reused the shot id")
	}
	// 128 low pixels = 189 points = 378 Retina pixels: sharper, never upscaled.
	if zoom.W != 378 || zoom.Scale >= low.Scale {
		t.Fatalf("zoom is %d px wide at %.3f pt/px; want 378 px, sharper than %.3f", zoom.W, zoom.Scale, low.Scale)
	}
	// A target read in the low shot and the same target read in the zoom
	// land within one low pixel of each other.
	g, _ := toGlobal(scr, low, 576, 288)
	px, ok := pixelOf(zoom, g)
	if !ok {
		t.Fatalf("global %v is not inside the zoom", g)
	}
	g2, _ := toGlobal(scr, zoom, px.X, px.Y)
	near(t, "low → zoom → global", g2, g, zoom.Scale)
	// The zoom's own corner is where the low shot said the region starts.
	c, _ := toGlobal(scr, zoom, 0, 0)
	near(t, "zoom corner", c, human.Point{X: 512 * low.Scale, Y: 256 * low.Scale}, 1)
}

func TestZoomRegionIsClampedToFrame(t *testing.T) {
	scr, reg := &fakeScreen{}, &shots{}
	ctx := context.Background()
	low, _, _ := take(ctx, scr, reg, shotRequest{})
	zoom, _, err := take(ctx, scr, reg, shotRequest{Base: low, Region: [4]float64{1000, 600, 200, 200}})
	if err != nil {
		t.Fatal(err)
	}
	if end := zoom.QX + zoom.QW; end > 1512.01 {
		t.Fatalf("region runs to x=%.1f, past the display", end)
	}
	if _, _, err := take(ctx, scr, reg, shotRequest{Base: low, Region: [4]float64{2000, 0, 10, 10}}); err == nil {
		t.Fatal("a region entirely outside the frame was accepted")
	}
}

func TestWindowShotFollowsMovesAndRefusesResize(t *testing.T) {
	scr := &fakeScreen{windows: []screen.Window{{ID: 7, App: "Notes", X: 100, Y: 80, Width: 800, Height: 600}}}
	reg := &shots{}
	sh, _, err := take(context.Background(), scr, reg, shotRequest{Window: 7})
	if err != nil {
		t.Fatal(err)
	}
	if sh.W != 1024 || sh.Scale != 800.0/1024 {
		t.Fatalf("window shot %dx%d at %.3f", sh.W, sh.H, sh.Scale)
	}
	before, _ := aim(scr, sh, 512, 384)
	scr.windows[0].X, scr.windows[0].Y = 300, 130
	after, err := aim(scr, sh, 512, 384)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "moved window", after, human.Point{X: before.X + 200, Y: before.Y + 50}, 0.01)

	scr.windows[0].Width = 700
	if _, err := aim(scr, sh, 512, 384); err == nil || !strings.Contains(err.Error(), "changed size") {
		t.Fatalf("resized window: err %v", err)
	}
}

func TestCoveredWindowPointIsRefused(t *testing.T) {
	scr := &fakeScreen{windows: []screen.Window{
		{ID: 9, App: "Mail", X: 0, Y: 0, Width: 400, Height: 400},
		{ID: 7, App: "Notes", X: 100, Y: 80, Width: 800, Height: 600},
	}}
	reg := &shots{}
	sh, _, _ := take(context.Background(), scr, reg, shotRequest{Window: 7})
	if _, err := aim(scr, sh, 10, 10); err == nil || !strings.Contains(err.Error(), "covered") {
		t.Fatalf("covered point: err %v", err)
	}
	if _, err := aim(scr, sh, 900, 600); err != nil {
		t.Fatalf("uncovered point: %v", err)
	}
}

func TestMarkLandsOnTarget(t *testing.T) {
	scr, reg := &fakeScreen{}, &shots{}
	ctx := context.Background()
	low, _, _ := take(ctx, scr, reg, shotRequest{})
	zoom, data, err := take(ctx, scr, reg, shotRequest{Base: low, Mark: &position{300, 200}})
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	g, _ := toGlobal(scr, low, 300, 200)
	c, _ := pixelOf(zoom, g)
	// The red arm sits 8 px right of the target; the target itself stays clear.
	r, gg, b, _ := img.At(int(c.X)+8, int(c.Y)).RGBA()
	if r>>8 < 180 || gg>>8 > 90 || b>>8 > 90 {
		t.Fatalf("no red crosshair arm next to the mark: rgb(%d, %d, %d)", r>>8, gg>>8, b>>8)
	}
	if r, _, _, _ := img.At(int(c.X), int(c.Y)).RGBA(); r>>8 < 150 {
		t.Fatal("the marked pixel itself is covered")
	}
}

func TestShrinkAveragesBoxes(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for x := 0; x < 4; x++ {
		for y := 0; y < 2; y++ {
			v := uint8(0)
			if x%2 == 0 {
				v = 200
			}
			src.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	dst := shrink(src, src.Rect, 2, 1)
	if got := dst.RGBAAt(0, 0).R; got != 100 {
		t.Fatalf("box average %d, want 100", got)
	}
}

func TestValidateBeforeActing(t *testing.T) {
	r := &runner{reg: &shots{}}
	x, y := 10.0, 10.0
	cases := map[string][]step{
		"no steps":        {},
		"unknown action":  {{Do: "jump"}},
		"move without xy": {{Do: "move"}},
		"half a point":    {{Do: "click", X: &x}},
		"unknown shot":    {{Do: "click", Shot: 3, X: &x, Y: &y}},
		"no shot":         {{Do: "click", X: &x, Y: &y}},
		"empty text":      {{Do: "type"}},
		"bad wait_for":    {{Do: "wait_for", Until: "soon"}},
	}
	for name, steps := range cases {
		if err := r.validate(steps, 0); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := r.validate([]step{{Do: "key", Keys: "enter"}, {Do: "wait_for", Until: "change"}}, 0); err != nil {
		t.Errorf("valid sequence refused: %v", err)
	}
}
