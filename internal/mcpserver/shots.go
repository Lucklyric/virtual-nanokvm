package mcpserver

import (
	"context"
	"fmt"
	"image"
	"math"
	"sync"

	"github.com/Lucklyric/virtual-nanokvm/internal/human"
	"github.com/Lucklyric/virtual-nanokvm/internal/screen"
)

const (
	defaultShotWidth = 1024 // a whole window or display, enough to find targets
	maxShotWidth     = 1568 // the largest image a model reads without its own downscale
	keepShots        = 32
	markZoom         = 200.0 // points around a mark when no region is given
)

// shot is one image the server returned. Every coordinate an agent sends
// names the shot it was read from, so zoomed and full images never mix up.
//
// A shot covers region q of a frame (the main display, or one window), in
// frame points. Pixel (px, py) of the image is the global point
// origin + q.min + (px + 0.5, py + 0.5) × scale, where origin is the frame's
// current position (a window may have moved since).
type shot struct {
	ID      int
	Window  uint32  // 0: main display
	PID     int     // the window's app
	App     string  // window's app, for messages
	FW, FH  float64 // frame size in points when taken
	QX, QY  float64 // region origin within the frame, points
	QW, QH  float64 // region size, points
	W, H    int     // image size, pixels
	Scale   float64 // points per image pixel
	FX, FY  float64 // frame origin when taken, global points
	Cropped bool
}

type shots struct {
	mu   sync.Mutex
	next int
	list []*shot
}

func (s *shots) add(sh *shot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	sh.ID = s.next
	s.list = append(s.list, sh)
	if len(s.list) > keepShots {
		s.list = s.list[1:]
	}
}

func (s *shots) get(id int) (*shot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id <= 0 {
		return nil, fmt.Errorf("give the shot id of the screenshot the coordinates come from")
	}
	for _, sh := range s.list {
		if sh.ID == id {
			return sh, nil
		}
	}
	return nil, fmt.Errorf("unknown shot %d (only the last %d are kept); take a new screenshot", id, keepShots)
}

func (s *shots) latest() *shot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.list) == 0 {
		return nil
	}
	return s.list[len(s.list)-1]
}

// frameNow returns the frame's current origin and size, and refuses when it
// changed size: the content has reflowed, so the image's pixels are stale.
func frameNow(scr Screen, sh *shot) (x, y float64, win *screen.Window, err error) {
	if sh.Window == 0 {
		w, h := scr.Bounds()
		if math.Abs(w-sh.FW) > 1 || math.Abs(h-sh.FH) > 1 {
			return 0, 0, nil, fmt.Errorf("the display changed size since shot %d; take a new screenshot", sh.ID)
		}
		return 0, 0, nil, nil
	}
	cur, err := scr.FindWindow(sh.Window, "")
	if err != nil {
		return 0, 0, nil, fmt.Errorf("window %d of shot %d is gone (closed or minimized); take a new screenshot", sh.Window, sh.ID)
	}
	if math.Abs(cur.Width-sh.FW) > 1 || math.Abs(cur.Height-sh.FH) > 1 {
		return 0, 0, nil, fmt.Errorf("window %d changed size since shot %d; take a new screenshot", sh.Window, sh.ID)
	}
	return cur.X, cur.Y, &cur, nil
}

// toGlobal maps pixel (x, y) of a shot to a global point.
func toGlobal(scr Screen, sh *shot, x, y float64) (human.Point, error) {
	if x < 0 || y < 0 || x >= float64(sh.W) || y >= float64(sh.H) {
		return human.Point{}, fmt.Errorf("(%.0f, %.0f) is outside shot %d, which is %dx%d", x, y, sh.ID, sh.W, sh.H)
	}
	ox, oy, _, err := frameNow(scr, sh)
	if err != nil {
		return human.Point{}, err
	}
	return human.Point{X: ox + sh.QX + (x+0.5)*sh.Scale, Y: oy + sh.QY + (y+0.5)*sh.Scale}, nil
}

// aim maps pixel (x, y) of a shot to a global point the mouse can act on. A
// point another window covers is refused, since the click would land there.
func aim(scr Screen, sh *shot, x, y float64) (human.Point, error) {
	p, err := toGlobal(scr, sh, x, y)
	if err != nil || sh.Window == 0 {
		return p, err
	}
	if top, ok := scr.WindowAt(p.X, p.Y); ok && top.ID != sh.Window {
		return p, fmt.Errorf("window %d (%s) is covered at that point by %s (window %d); bring it to the front first",
			sh.Window, sh.App, top.App, top.ID)
	}
	return p, nil
}

// pixelOf maps a global point back to a pixel of the shot, with the frame
// where it is now. ok is false outside the image or when the frame changed.
func pixelOf(scr Screen, sh *shot, p human.Point) (position, bool) {
	ox, oy, _, err := frameNow(scr, sh)
	if err != nil {
		return position{}, false
	}
	px := math.Floor((p.X - ox - sh.QX) / sh.Scale)
	py := math.Floor((p.Y - oy - sh.QY) / sh.Scale)
	return position{px, py}, px >= 0 && py >= 0 && px < float64(sh.W) && py < float64(sh.H)
}

// shotRequest says what to capture.
type shotRequest struct {
	Window uint32     // frame for a new full shot; 0 = main display
	Base   *shot      // zoom into this shot's frame
	Region [4]float64 // x, y, w, h in Base's pixels; zero = whole frame
	Mark   *position  // crosshair at this pixel of Base
	Width  int
}

// take captures a shot and returns its JPEG.
func take(ctx context.Context, scr Screen, reg *shots, req shotRequest) (*shot, []byte, error) {
	sh := &shot{Window: req.Window}
	var win *screen.Window
	if req.Base != nil {
		b := req.Base
		ox, oy, w, err := frameNow(scr, b)
		if err != nil {
			return nil, nil, err
		}
		sh.Window, sh.PID, sh.App, sh.FW, sh.FH, sh.FX, sh.FY, win = b.Window, b.PID, b.App, b.FW, b.FH, ox, oy, w
		r := req.Region
		if r[2] <= 0 || r[3] <= 0 {
			if req.Mark == nil {
				return nil, nil, fmt.Errorf("give a region [x, y, width, height] in shot %d's pixels, or a mark", b.ID)
			}
			half := markZoom / 2 / b.Scale
			r = [4]float64{req.Mark.X - half, req.Mark.Y - half, 2 * half, 2 * half}
		}
		sh.QX, sh.QY = b.QX+r[0]*b.Scale, b.QY+r[1]*b.Scale
		sh.QW, sh.QH = r[2]*b.Scale, r[3]*b.Scale
		sh.Cropped = true
	} else if req.Window != 0 {
		w, err := scr.FindWindow(req.Window, "")
		if err != nil {
			return nil, nil, err
		}
		win = &w
		sh.PID, sh.App, sh.FW, sh.FH, sh.FX, sh.FY = w.PID, w.App, w.Width, w.Height, w.X, w.Y
		sh.QW, sh.QH = w.Width, w.Height
	} else {
		sh.FW, sh.FH = scr.Bounds()
		sh.QW, sh.QH = sh.FW, sh.FH
	}

	// Keep the region inside the frame.
	x0, y0 := math.Max(sh.QX, 0), math.Max(sh.QY, 0)
	x1, y1 := math.Min(sh.QX+sh.QW, sh.FW), math.Min(sh.QY+sh.QH, sh.FH)
	if x1-x0 < 1 || y1-y0 < 1 {
		return nil, nil, fmt.Errorf("the region is outside the frame")
	}

	raw, err := scr.Capture(ctx, sh.Window)
	if err != nil {
		return nil, nil, err
	}
	rb := raw.Bounds()
	k := float64(rb.Dx()) / sh.FW // raw pixels per point (2 on a Retina display)
	crop := image.Rect(
		rb.Min.X+int(math.Floor(x0*k)), rb.Min.Y+int(math.Floor(y0*k)),
		rb.Min.X+int(math.Ceil(x1*k)), rb.Min.Y+int(math.Ceil(y1*k)),
	).Intersect(rb)
	if crop.Empty() {
		return nil, nil, fmt.Errorf("the region is outside the captured image")
	}
	// The region in points, exactly as cropped.
	sh.QX, sh.QY = float64(crop.Min.X-rb.Min.X)/k, float64(crop.Min.Y-rb.Min.Y)/k
	sh.QW, sh.QH = float64(crop.Dx())/k, float64(crop.Dy())/k

	width := req.Width
	if width <= 0 {
		width = defaultShotWidth
		if sh.Cropped {
			width = maxShotWidth
		}
	}
	sh.W = min(width, maxShotWidth, crop.Dx()) // never upscale
	sh.H = max(1, int(math.Round(float64(sh.W)*float64(crop.Dy())/float64(crop.Dx()))))
	sh.H = min(sh.H, crop.Dy())
	sh.Scale = sh.QW / float64(sh.W)
	img := shrink(raw, crop, sh.W, sh.H)

	if req.Mark != nil && req.Base != nil {
		b := req.Base
		gx, gy := b.QX+(req.Mark.X+0.5)*b.Scale, b.QY+(req.Mark.Y+0.5)*b.Scale // frame points
		mark(img, int((gx-sh.QX)/sh.Scale), int((gy-sh.QY)/sh.Scale))
	}
	if win != nil {
		sh.App = win.App
	}
	data, err := encodeJPEG(img)
	if err != nil {
		return nil, nil, err
	}
	reg.add(sh)
	return sh, data, nil
}
