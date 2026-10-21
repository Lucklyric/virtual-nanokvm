package mcpserver

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
)

// shrink returns the crop r of src scaled down to w×h with a box filter, which
// keeps small text readable where point sampling would smear it. w and h must
// not exceed r's size.
func shrink(src image.Image, r image.Rectangle, w, h int) *image.RGBA {
	pix, stride, origin := rgbaPix(src)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xs := make([]int, w+1)
	for i := range xs {
		xs[i] = r.Min.X + i*r.Dx()/w
	}
	for dy := 0; dy < h; dy++ {
		y0, y1 := r.Min.Y+dy*r.Dy()/h, r.Min.Y+(dy+1)*r.Dy()/h
		for dx := 0; dx < w; dx++ {
			x0, x1 := xs[dx], xs[dx+1]
			var sr, sg, sb, n int
			for y := y0; y < y1; y++ {
				row := (y-origin.Y)*stride + (x0-origin.X)*4
				for x := x0; x < x1; x++ {
					sr += int(pix[row])
					sg += int(pix[row+1])
					sb += int(pix[row+2])
					row += 4
					n++
				}
			}
			o := dy*dst.Stride + dx*4
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = uint8(sr/n), uint8(sg/n), uint8(sb/n), 255
		}
	}
	return dst
}

// rgbaPix exposes 8-bit RGBA-ordered pixels. Captures are opaque, so NRGBA
// and RGBA share a layout here; anything else is converted once.
func rgbaPix(src image.Image) ([]uint8, int, image.Point) {
	switch m := src.(type) {
	case *image.RGBA:
		return m.Pix, m.Stride, m.Rect.Min
	case *image.NRGBA:
		return m.Pix, m.Stride, m.Rect.Min
	}
	m := image.NewRGBA(src.Bounds())
	draw.Draw(m, m.Rect, src, m.Rect.Min, draw.Src)
	return m.Pix, m.Stride, m.Rect.Min
}

// mark draws a crosshair centered on (x, y), with a gap in the middle so the
// pixel under it stays visible.
func mark(img *image.RGBA, x, y int) {
	red, white := color.RGBA{230, 0, 0, 255}, color.RGBA{255, 255, 255, 255}
	for d := 4; d <= 14; d++ {
		for _, p := range [][2]int{{x - d, y}, {x + d, y}, {x, y - d}, {x, y + d}} {
			for t := -2; t <= 2; t++ {
				c := red // 3 px wide so it survives JPEG color subsampling
				if t == -2 || t == 2 {
					c = white // a light edge keeps the mark visible on red or dark areas
				}
				if p[0] == x {
					img.SetRGBA(p[0]+t, p[1], c)
				} else {
					img.SetRGBA(p[0], p[1]+t, c)
				}
			}
		}
	}
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var b bytes.Buffer
	err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
	return b.Bytes(), err
}

// changed counts preview pixels inside r whose luma moved by more than a
// small threshold, between two previews of the main display. r is given as
// fractions of the display (0..1). JPEG noise stays below the threshold.
func changed(a, b []byte, r [4]float64) (int, error) {
	ia, err := jpeg.Decode(bytes.NewReader(a))
	if err != nil {
		return 0, err
	}
	ib, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	ya, ok1 := ia.(*image.YCbCr)
	yb, ok2 := ib.(*image.YCbCr)
	if !ok1 || !ok2 || ya.Rect != yb.Rect {
		return math.MaxInt32, nil // the display changed size
	}
	w, h := float64(ya.Rect.Dx()), float64(ya.Rect.Dy())
	x0, y0 := max(int(r[0]*w), 0), max(int(r[1]*h), 0)
	x1 := min(int(math.Ceil((r[0]+r[2])*w)), ya.Rect.Dx())
	y1 := min(int(math.Ceil((r[1]+r[3])*h)), ya.Rect.Dy())
	n := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			d := int(ya.Y[ya.YOffset(x, y)]) - int(yb.Y[yb.YOffset(x, y)])
			if d > 24 || d < -24 {
				n++
			}
		}
	}
	return n, nil
}

// regionPixels is how many pixels of preview p fall inside r (fractions).
func regionPixels(p []byte, r [4]float64) int {
	c, err := jpeg.DecodeConfig(bytes.NewReader(p))
	if err != nil {
		return 0
	}
	return int(r[2] * float64(c.Width) * r[3] * float64(c.Height))
}
