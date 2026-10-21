// Package mcpserver exposes the virtual keyboard, mouse and screen as MCP tools.
//
// Coordinates come in two spaces:
//   - global: display points, shared by every screen and window (what the
//     mouse moves in)
//   - relative: pixels of the screenshot of a frame, which is either the main
//     display or one app window (window_id / app)
//
// global = frame origin + relative × scale. Tools take relative coordinates for
// the frame they name, so an agent can click straight from a screenshot.
package mcpserver

import (
	"context"
	"fmt"
	"math"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Lucklyric/virtual-nanokvm/internal/doctor"
	"github.com/Lucklyric/virtual-nanokvm/internal/human"
	"github.com/Lucklyric/virtual-nanokvm/internal/screen"
)

// Screen is what the server needs from the display.
type Screen interface {
	Screenshot(ctx context.Context, width int) ([]byte, error)
	Bounds() (width, height float64) // main display, in points
	Windows() []screen.Window
	FindWindow(id uint32, app string) (screen.Window, error)
	WindowAt(x, y float64) (screen.Window, bool)
	WindowShot(ctx context.Context, id uint32, width int) ([]byte, error)
}

// maxShotWidth caps screenshot width so images stay small enough for a model.
const maxShotWidth = 1568

// frame is the area a screenshot covers, in global points.
type frame struct {
	x, y, w, h float64
	window     *screen.Window
}

func (f frame) scale() float64 { return f.w / math.Min(f.w, maxShotWidth) }

func (f frame) imageSize() (float64, float64) {
	return math.Round(f.w / f.scale()), math.Round(f.h / f.scale())
}

func (f frame) name() string {
	if f.window == nil {
		return "main display screenshot"
	}
	return fmt.Sprintf("window %d screenshot", f.window.ID)
}

// toGlobal converts screenshot pixels of this frame to a global point.
func (f frame) toGlobal(x, y float64) (human.Point, error) {
	w, h := f.imageSize()
	if x < 0 || y < 0 || x >= w || y >= h {
		return human.Point{}, fmt.Errorf("(%.0f, %.0f) is outside the %.0fx%.0f %s", x, y, w, h, f.name())
	}
	k := f.scale()
	return human.Point{X: f.x + x*k, Y: f.y + y*k}, nil
}

// locate describes a global point in both spaces.
func (f frame) locate(p human.Point) location {
	loc := location{Global: position{math.Round(p.X), math.Round(p.Y)}}
	if f.window != nil {
		loc.WindowID = f.window.ID
	}
	k := f.scale()
	rx, ry := (p.X-f.x)/k, (p.Y-f.y)/k
	if w, h := f.imageSize(); rx >= 0 && ry >= 0 && rx < w && ry < h {
		loc.Relative = &position{math.Round(rx), math.Round(ry)}
	}
	return loc
}

type position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type location struct {
	Global   position  `json:"global" jsonschema:"display points, valid across all screens and windows"`
	Relative *position `json:"relative,omitempty" jsonschema:"pixels in the screenshot of the frame used (main display, or window_id); omitted when the point is outside it"`
	WindowID uint32    `json:"window_id,omitempty" jsonschema:"the window the relative coordinates belong to"`
}

// target picks the frame: one app window, or the main display.
type target struct {
	WindowID uint32 `json:"window_id,omitempty" jsonschema:"use this window's screenshot coordinates (see list_windows)"`
	App      string `json:"app,omitempty" jsonschema:"use the frontmost window of this app (name substring), e.g. Safari"`
}

type shotArgs struct {
	target
}

type shotInfo struct {
	Frame       string   `json:"frame" jsonschema:"main_display or window"`
	WindowID    uint32   `json:"window_id,omitempty"`
	App         string   `json:"app,omitempty"`
	Title       string   `json:"title,omitempty"`
	Origin      position `json:"origin" jsonschema:"global point of the image's top-left pixel"`
	Width       float64  `json:"width" jsonschema:"frame width in global points"`
	Height      float64  `json:"height" jsonschema:"frame height in global points"`
	ImageWidth  float64  `json:"image_width"`
	ImageHeight float64  `json:"image_height"`
	Scale       float64  `json:"scale" jsonschema:"global points per image pixel: global = origin + pixel * scale"`
	Cursor      location `json:"cursor"`
}

type moveArgs struct {
	target
	X    float64 `json:"x" jsonschema:"target x in the frame's screenshot pixels"`
	Y    float64 `json:"y" jsonschema:"target y in the frame's screenshot pixels"`
	Size float64 `json:"size,omitempty" jsonschema:"approximate size of the target in screenshot pixels (default 20); smaller targets get a slower, more careful approach"`
}

type clickArgs struct {
	target
	X      *float64 `json:"x,omitempty" jsonschema:"move here first (frame screenshot pixels); omit to click in place"`
	Y      *float64 `json:"y,omitempty" jsonschema:"move here first (frame screenshot pixels); omit to click in place"`
	Button string   `json:"button,omitempty" jsonschema:"left (default), right or middle"`
	Count  int      `json:"count,omitempty" jsonschema:"1 for a click (default), 2 for a double click"`
}

type dragArgs struct {
	target
	X float64 `json:"x" jsonschema:"drop x in the frame's screenshot pixels"`
	Y float64 `json:"y" jsonschema:"drop y in the frame's screenshot pixels"`
}

type scrollArgs struct {
	DY int `json:"dy,omitempty" jsonschema:"wheel notches; positive scrolls down"`
	DX int `json:"dx,omitempty" jsonschema:"wheel notches; positive scrolls right"`
}

type typeArgs struct {
	Text string  `json:"text" jsonschema:"text to type (US keyboard characters only)"`
	WPM  float64 `json:"wpm,omitempty" jsonschema:"typing speed in words per minute (default 70)"`
}

type keyArgs struct {
	Keys string `json:"keys" jsonschema:"key or combination, e.g. enter, cmd+c, cmd+shift+4, ctrl+left"`
}

type windowList struct {
	Windows []screen.Window `json:"windows" jsonschema:"on-screen app windows, frontmost first; x, y, width, height in global points"`
}

type empty struct{}

type doctorArgs struct {
	Fix bool `json:"fix,omitempty" jsonschema:"ask macOS for missing permissions (opens the system dialogs)"`
}

type doctorReport struct {
	Checks []doctor.Check `json:"checks"`
}

// New builds the MCP server. bridgeSocket is where doctor looks for the
// driver input bridge.
func New(e *human.Engine, scr Screen, bridgeSocket string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "virtual-nanokvm", Version: "0.1.0"}, nil)

	frameFor := func(t target) (frame, error) {
		if t.WindowID == 0 && t.App == "" {
			w, h := scr.Bounds()
			return frame{w: w, h: h}, nil
		}
		win, err := scr.FindWindow(t.WindowID, t.App)
		if err != nil {
			return frame{}, err
		}
		return frame{x: win.X, y: win.Y, w: win.Width, h: win.Height, window: &win}, nil
	}

	// aim converts frame pixels to a global point. For a window it re-reads
	// the window's current bounds (it may have moved since the screenshot) and
	// refuses points another window covers, since the click would land there.
	aim := func(t target, x, y float64) (frame, human.Point, error) {
		f, err := frameFor(t)
		if err != nil {
			return f, human.Point{}, err
		}
		p, err := f.toGlobal(x, y)
		if err != nil || f.window == nil {
			return f, p, err
		}
		if top, ok := scr.WindowAt(p.X, p.Y); ok && top.ID != f.window.ID {
			return f, p, fmt.Errorf("window %d (%s) is covered at that point by %s (window %d); bring it to the front first",
				f.window.ID, f.window.App, top.App, top.ID)
		}
		return f, p, nil
	}

	cursor := func(f frame) (location, error) {
		p, err := e.Cursor.Position()
		return f.locate(p), err
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "doctor",
		Description: "Check setup: macOS version, Accessibility and Screen Recording permissions, whether the display can be captured, driver input, Tailscale. Run it first when a tool fails, and tell the user any FIX lines.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in doctorArgs) (*mcp.CallToolResult, doctorReport, error) {
		checks := doctor.Run(ctx, bridgeSocket, in.Fix)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: doctor.Format(checks)}}}, doctorReport{checks}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_windows",
		Description: "List on-screen app windows, frontmost first, with ids and global bounds.",
	}, func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, windowList, error) {
		return nil, windowList{scr.Windows()}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "screenshot",
		Description: "Capture the main display, or one app window (window_id or app) even when other windows cover it. " +
			"Returns the image plus its origin and scale, so global = origin + pixel × scale. " +
			"Pass the same window_id or app to the mouse tools to use this image's pixels directly.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in shotArgs) (*mcp.CallToolResult, shotInfo, error) {
		f, err := frameFor(in.target)
		if err != nil {
			return nil, shotInfo{}, err
		}
		iw, ih := f.imageSize()
		var img []byte
		if f.window == nil {
			img, err = scr.Screenshot(ctx, int(iw))
		} else {
			img, err = scr.WindowShot(ctx, f.window.ID, int(iw))
		}
		if err != nil {
			return nil, shotInfo{}, err
		}
		c, err := cursor(f)
		if err != nil {
			return nil, shotInfo{}, err
		}
		info := shotInfo{
			Frame: "main_display", Origin: position{f.x, f.y}, Width: f.w, Height: f.h,
			ImageWidth: iw, ImageHeight: ih, Scale: f.scale(), Cursor: c,
		}
		label := "main display"
		if f.window != nil {
			info.Frame, info.WindowID, info.App, info.Title = "window", f.window.ID, f.window.App, f.window.Title
			label = fmt.Sprintf("window %d %q (%s)", f.window.ID, f.window.Title, f.window.App)
		}
		note := fmt.Sprintf("%s: image %.0fx%.0f, origin (%.0f, %.0f) global, scale %.3f; cursor global (%.0f, %.0f)",
			label, iw, ih, f.x, f.y, f.scale(), c.Global.X, c.Global.Y)
		if c.Relative != nil {
			note += fmt.Sprintf(", relative (%.0f, %.0f)", c.Relative.X, c.Relative.Y)
		} else {
			note += ", outside this image"
		}
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.ImageContent{Data: img, MIMEType: "image/jpeg"},
			&mcp.TextContent{Text: note},
		}}, info, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "cursor_position",
		Description: "Return the cursor position, global and relative to the main display or a window.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in shotArgs) (*mcp.CallToolResult, location, error) {
		f, err := frameFor(in.target)
		if err != nil {
			return nil, location{}, err
		}
		c, err := cursor(f)
		return nil, c, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "mouse_move",
		Description: "Move the mouse to a point along a human-like path (curved, accelerating then slowing, with a small overshoot on long moves).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in moveArgs) (*mcp.CallToolResult, location, error) {
		f, p, err := aim(in.target, in.X, in.Y)
		if err != nil {
			return nil, location{}, err
		}
		got, err := e.MoveTo(ctx, p, in.Size*f.scale())
		return nil, f.locate(got), err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "click",
		Description: "Click a mouse button, optionally moving to (x, y) first with a human-like path.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in clickArgs) (*mcp.CallToolResult, location, error) {
		b, err := button(in.Button)
		if err != nil {
			return nil, location{}, err
		}
		if (in.X == nil) != (in.Y == nil) {
			return nil, location{}, fmt.Errorf("give both x and y, or neither")
		}
		f, err := frameFor(in.target)
		if err != nil {
			return nil, location{}, err
		}
		if in.X != nil {
			var p human.Point
			if f, p, err = aim(in.target, *in.X, *in.Y); err != nil {
				return nil, location{}, err
			}
			if _, err := e.MoveTo(ctx, p, 0); err != nil {
				return nil, location{}, err
			}
		}
		count := in.Count
		if count <= 0 {
			count = 1
		}
		if err := e.Click(ctx, b, count); err != nil {
			return nil, location{}, err
		}
		c, err := cursor(f)
		return nil, c, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "drag",
		Description: "Hold the left button at the current position, move to (x, y) and release.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dragArgs) (*mcp.CallToolResult, location, error) {
		f, p, err := aim(in.target, in.X, in.Y)
		if err != nil {
			return nil, location{}, err
		}
		got, err := e.Drag(ctx, human.Left, p)
		return nil, f.locate(got), err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "scroll",
		Description: "Turn the scroll wheel at the current cursor position.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in scrollArgs) (*mcp.CallToolResult, empty, error) {
		return nil, empty{}, e.Scroll(ctx, in.DX, in.DY)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "type_text",
		Description: "Type text on the virtual keyboard with human rhythm.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in typeArgs) (*mcp.CallToolResult, empty, error) {
		return nil, empty{}, e.Type(ctx, in.Text, in.WPM)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "key",
		Description: "Press a key or key combination such as enter, escape, cmd+c, cmd+shift+4.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in keyArgs) (*mcp.CallToolResult, empty, error) {
		return nil, empty{}, e.Press(ctx, in.Keys)
	})

	return s
}

func button(name string) (human.Button, error) {
	switch name {
	case "", "left":
		return human.Left, nil
	case "right":
		return human.Right, nil
	case "middle":
		return human.Middle, nil
	}
	return 0, fmt.Errorf("unknown button %q", name)
}
