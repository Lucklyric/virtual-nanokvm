// Package mcpserver exposes the virtual keyboard, mouse and screen as MCP tools.
//
// Every screenshot gets a shot id. Coordinates an agent sends are pixels of a
// named shot, so a quick low-detail look and a sharp zoom of one corner never
// get mixed up. The server maps them to global display points (the space the
// mouse moves in), following a window that moved since the shot was taken.
package mcpserver

import (
	"context"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Lucklyric/virtual-nanokvm/internal/doctor"
	"github.com/Lucklyric/virtual-nanokvm/internal/human"
	"github.com/Lucklyric/virtual-nanokvm/internal/screen"
)

// Screen is what the server needs from the display.
type Screen interface {
	Bounds() (width, height float64) // main display, in points
	Windows() []screen.Window
	FindWindow(id uint32, app string) (screen.Window, error)
	WindowAt(x, y float64) (screen.Window, bool)
	// Capture grabs the main display (window 0) or one window at full resolution.
	Capture(ctx context.Context, window uint32) (image.Image, error)
	// Preview is a fast small JPEG of the main display, to see whether it moved.
	Preview(width int) ([]byte, error)
}

type position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type shotInfo struct {
	Shot        int        `json:"shot" jsonschema:"id to pass with coordinates read from this image"`
	Frame       string     `json:"frame" jsonschema:"main_display or window"`
	WindowID    uint32     `json:"window_id,omitempty"`
	App         string     `json:"app,omitempty"`
	ImageWidth  int        `json:"image_width"`
	ImageHeight int        `json:"image_height"`
	Region      [4]float64 `json:"region" jsonschema:"x, y, width, height of the frame this image covers, in points"`
	Origin      position   `json:"origin" jsonschema:"global point of the image's top-left corner"`
	Scale       float64    `json:"scale" jsonschema:"points per image pixel"`
	Cursor      *position  `json:"cursor,omitempty" jsonschema:"cursor in this image's pixels; omitted when outside it"`
}

func info(scr Screen, e *human.Engine, sh *shot) shotInfo {
	in := shotInfo{
		Shot: sh.ID, Frame: "main_display", WindowID: sh.Window, App: sh.App,
		ImageWidth: sh.W, ImageHeight: sh.H, Region: [4]float64{sh.QX, sh.QY, sh.QW, sh.QH},
		Origin: position{sh.FX + sh.QX, sh.FY + sh.QY}, Scale: sh.Scale,
	}
	if sh.Window != 0 {
		in.Frame = "window"
	}
	if p, err := e.Cursor.Position(); err == nil {
		if px, ok := pixelOf(scr, sh, p); ok {
			in.Cursor = &px
		}
	}
	return in
}

func (in shotInfo) note() string {
	what := "main display"
	if in.Frame == "window" {
		what = fmt.Sprintf("window %d (%s)", in.WindowID, in.App)
	}
	s := fmt.Sprintf("shot %d: %s, %dx%d px, %.2f points per px", in.Shot, what, in.ImageWidth, in.ImageHeight, in.Scale)
	if in.Region[0] > 0 || in.Region[1] > 0 {
		s += fmt.Sprintf(", zoom of region (%.0f, %.0f, %.0f×%.0f pt)", in.Region[0], in.Region[1], in.Region[2], in.Region[3])
	}
	if in.Cursor != nil {
		s += fmt.Sprintf("; cursor at (%.0f, %.0f)", in.Cursor.X, in.Cursor.Y)
	} else {
		s += "; cursor outside this image"
	}
	return s + fmt.Sprintf(". Use shot %d with coordinates from this image.", in.Shot)
}

type shotArgs struct {
	WindowID uint32    `json:"window_id,omitempty" jsonschema:"capture this window, even when covered (see list_windows)"`
	App      string    `json:"app,omitempty" jsonschema:"capture the frontmost window of this app (name substring), e.g. Safari"`
	Shot     int       `json:"shot,omitempty" jsonschema:"zoom into this earlier shot: region and mark are in its pixels"`
	Region   []float64 `json:"region,omitempty" jsonschema:"x, y, width, height in the pixels of shot, to capture sharper"`
	Mark     *position `json:"mark,omitempty" jsonschema:"draw a crosshair at this pixel of shot, to check aim before clicking; without region, zooms around it"`
	Width    int       `json:"width,omitempty" jsonschema:"image width in pixels: default 1024 for a whole frame (low detail), 1568 for a zoom; max 1568"`
}

type cursorArgs struct {
	Shot int `json:"shot,omitempty" jsonschema:"also give the position in this shot's pixels (default: the latest shot)"`
}

type cursorInfo struct {
	Global position  `json:"global" jsonschema:"display points"`
	Shot   int       `json:"shot,omitempty"`
	Pixel  *position `json:"pixel,omitempty" jsonschema:"in that shot's pixels; omitted when outside it"`
}

type modeArg struct {
	Mode string `json:"mode,omitempty" jsonschema:"foreground (default): the real cursor and keyboard, human-like; background: act on the shot's app without the cursor or bringing it forward (controls pressed through Accessibility, keys sent to that app). Use background when the person asks for it"`
}

type observeArg struct {
	Observe bool `json:"observe,omitempty" jsonschema:"after acting, wait for the screen to settle and return a new low-detail shot of the same window or display"`
}

type moveArgs struct {
	Shot int     `json:"shot" jsonschema:"shot the coordinates come from"`
	X    float64 `json:"x" jsonschema:"pixel x in that shot"`
	Y    float64 `json:"y" jsonschema:"pixel y in that shot"`
	Size float64 `json:"size,omitempty" jsonschema:"rough target size in that shot's pixels; small targets get a slower, careful approach"`
	observeArg
	modeArg
}

type clickArgs struct {
	Shot   int      `json:"shot,omitempty" jsonschema:"shot the coordinates come from; needed with x, y"`
	X      *float64 `json:"x,omitempty" jsonschema:"move here first (pixel x in that shot); omit to click in place"`
	Y      *float64 `json:"y,omitempty" jsonschema:"move here first (pixel y in that shot)"`
	Size   float64  `json:"size,omitempty" jsonschema:"rough target size in that shot's pixels"`
	Button string   `json:"button,omitempty" jsonschema:"left (default) or right"`
	Count  int      `json:"count,omitempty" jsonschema:"1 for a click (default), 2 for a double click"`
	observeArg
	modeArg
}

type dragArgs struct {
	Shot int     `json:"shot" jsonschema:"shot the coordinates come from"`
	X    float64 `json:"x" jsonschema:"drop pixel x in that shot"`
	Y    float64 `json:"y" jsonschema:"drop pixel y in that shot"`
	observeArg
	modeArg
}

type scrollArgs struct {
	DY int `json:"dy,omitempty" jsonschema:"wheel notches; positive scrolls down"`
	DX int `json:"dx,omitempty" jsonschema:"wheel notches; positive scrolls right"`
	observeArg
	modeArg
}

type typeArgs struct {
	Text string  `json:"text" jsonschema:"text to type (US keyboard characters only)"`
	WPM  float64 `json:"wpm,omitempty" jsonschema:"typing speed in words per minute (default 70)"`
	observeArg
	modeArg
}

type keyArgs struct {
	Keys string `json:"keys" jsonschema:"key or combination, e.g. enter, cmd+c, cmd+shift+4, ctrl+left"`
	observeArg
	modeArg
}

type actArgs struct {
	Shot    int               `json:"shot,omitempty" jsonschema:"default shot for the steps' coordinates; with recipe, a whole-window shot of the app"`
	Recipe  string            `json:"recipe,omitempty" jsonschema:"run this saved recipe of the shot's app first (see recipes); steps, if any, run after it"`
	Args    map[string]string `json:"args,omitempty" jsonschema:"values for the recipe's placeholders, e.g. {\"text2\": \"weather\"}"`
	Steps   []step            `json:"steps,omitempty" jsonschema:"actions in order; the sequence stops at the first failure"`
	Observe *bool             `json:"observe,omitempty" jsonschema:"return a new low-detail shot after the steps (default true)"`
	Width   int               `json:"width,omitempty" jsonschema:"width of that shot (default 1024)"`
	MaxAge  int               `json:"max_age,omitempty" jsonschema:"refuse coordinates from shots older than this many milliseconds, for targets that move (characters, live feeds); default any age"`
	modeArg
}

type actReport struct {
	Steps []string  `json:"steps" jsonschema:"what each step did, in order"`
	Error string    `json:"error,omitempty"`
	Shot  *shotInfo `json:"shot,omitempty" jsonschema:"the observation shot"`
}

type listArgs struct {
	App string `json:"app,omitempty" jsonschema:"show window titles for this app only (name substring); other apps are listed without titles"`
}

type lessonArgs struct {
	App  string   `json:"app,omitempty" jsonschema:"the app the lessons are about, e.g. Safari; all for rules that hold in every app; omit to list the apps that have lessons, tasks or recipes"`
	Add  string   `json:"add,omitempty" jsonschema:"a new lesson: what slowed you down and what worked, one line, no personal content"`
	Set  []string `json:"set,omitempty" jsonschema:"replace all lessons for this app, to merge, rewrite or drop lines; keep a line's v tag when you keep the line"`
	Done string   `json:"done,omitempty" jsonschema:"the task just finished, in a few generic words (no names, queries or window text); records what it cost under the current lessons"`
}

type lessonReport struct {
	App      string        `json:"app"`
	Version  int           `json:"version" jsonschema:"bumped by every set (a revision); each lesson line is tagged with the version it was added under"`
	Lessons  []string      `json:"lessons"`
	Rules    []string      `json:"rules_all,omitempty" jsonschema:"the lessons of app all, which hold in every app"`
	Apps     []appSummary  `json:"apps,omitempty" jsonschema:"without app: every app with lessons, tasks or recipes"`
	Tasks    []task        `json:"recent_tasks,omitempty" jsonschema:"the last finished tasks in this app and what they cost"`
	Versions []versionStat `json:"by_version,omitempty" jsonschema:"average cost of the tasks run under each lessons version"`
	Review   string        `json:"review,omitempty" jsonschema:"set when the lessons are due for a revision"`
	Stats    counters      `json:"session_stats" jsonschema:"what this session has done so far"`
}

type recipeArgs struct {
	App      string `json:"app,omitempty" jsonschema:"list or delete recipes of this app"`
	Save     string `json:"save,omitempty" jsonschema:"save steps as a recipe with this name (lowercase, digits, _); the same name replaces it"`
	When     string `json:"when,omitempty" jsonschema:"save: the situation it is for, generic, e.g. Safari window with any page open"`
	Shot     int    `json:"shot,omitempty" jsonschema:"save: the default shot of the steps' coordinates, as in act"`
	Steps    []step `json:"steps,omitempty" jsonschema:"save: the steps that worked, exactly as sent to act"`
	KeepText bool   `json:"keep_text,omitempty" jsonschema:"save: keep typed text as is (only for fixed, non-personal text); otherwise it becomes {{textN}} placeholders"`
	Delete   string `json:"delete,omitempty" jsonschema:"delete the recipe with this name"`
}

type recipeReport struct {
	App     string   `json:"app"`
	Recipes []recipe `json:"recipes"`
}

// Options configure the server.
type Options struct {
	BridgeSocket string     // where doctor looks for the driver input bridge
	AllowDisplay bool       // allow whole-display screenshots; off, only app windows are captured
	Background   Background // nil: background mode is unavailable
}

const displayOff = "whole-display screenshots are off: name the app (app or window_id). " +
	"The person can allow them with VNKVM_ALLOW_DISPLAY=1"

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

// New builds the MCP server.
func New(e *human.Engine, scr Screen, opt Options) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "virtual-nanokvm", Version: "0.6.0"}, nil)
	reg := &shots{}
	st := &stats{}
	// Every tool call is a model turn: the cost the lessons try to bring down.
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				st.call()
			}
			return next(ctx, method, req)
		}
	})

	// do runs steps, then optionally looks again. Every action tool uses it.
	do := func(ctx context.Context, steps []step, defShot int, observe bool, width int, batch bool, mode string, maxAge int) (*mcp.CallToolResult, actReport, error) {
		r := &runner{e: e, scr: scr, reg: reg, ax: opt.Background, maxAge: time.Duration(maxAge) * time.Millisecond}
		switch mode {
		case "", "foreground":
		case "background":
			if opt.Background == nil {
				return nil, actReport{}, fmt.Errorf("background mode is not available in this server")
			}
			r.bg = opt.Background
		default:
			return nil, actReport{}, fmt.Errorf("mode is foreground or background")
		}
		if err := r.validate(steps, defShot); err != nil {
			return nil, actReport{}, err
		}
		lines, runErr := r.run(ctx, steps)
		hint := st.action(!batch, len(lines), runErr)
		st.predicted(r.expects, r.misses, false)
		rep := actReport{Steps: lines}
		res := &mcp.CallToolResult{}
		text := strings.Join(lines, "\n")
		if runErr != nil {
			rep.Error = runErr.Error()
			res.IsError = true
			text += "\n" + runErr.Error()
			if tookControl(runErr) {
				text += "\nThe person moved the mouse. Everything is released. Stop and ask before continuing."
			}
			if r.misses > 0 {
				text += "\nThe screen did not do what the plan predicted; the steps after the expect did not run. Plan again from the new shot."
			}
		}
		if observe && ctx.Err() == nil {
			req := shotRequest{Width: width}
			base := r.last
			if base == nil {
				base = reg.latest()
			}
			var lookErr error
			switch {
			case base != nil && base.Window != 0:
				if _, err := scr.FindWindow(base.Window, ""); err == nil {
					req.Window = base.Window
				} else if !opt.AllowDisplay {
					lookErr = fmt.Errorf("window %d is gone; take a screenshot of the app now in front", base.Window)
				}
			case base == nil && !opt.AllowDisplay:
				lookErr = fmt.Errorf("no app screenshot yet; take one with app")
			}
			settled := ""
			if last := steps[len(steps)-1]; lookErr == nil && ((last.Do != "wait_for" && last.Do != "expect") || last.Until != "stable") {
				settled, _ = r.settle(ctx, settleTimeout, nil)
			}
			if lookErr != nil {
				text += "\nno observation: " + lookErr.Error()
			} else if sh, img, err := take(ctx, scr, reg, req); err == nil {
				in := info(scr, e, sh)
				rep.Shot = &in
				res.Content = append(res.Content, &mcp.ImageContent{Data: img, MIMEType: "image/jpeg"})
				text += "\nobserved" + settled + ": " + in.note()
			} else {
				text += "\nobservation failed: " + err.Error()
			}
		}
		if hint != "" {
			text += "\n" + hint
		}
		res.Content = append(res.Content, &mcp.TextContent{Text: text})
		return res, rep, nil
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "doctor",
		Description: "Check setup: macOS version, Accessibility and Screen Recording permissions, whether the display can be captured, driver input, Tailscale. Run it first when a tool fails, and tell the user any FIX lines.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in doctorArgs) (*mcp.CallToolResult, doctorReport, error) {
		checks := doctor.Run(ctx, opt.BridgeSocket, in.Fix)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: doctor.Format(checks)}}}, doctorReport{checks}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_windows",
		Description: "List on-screen app windows, frontmost first, with ids and global bounds. Titles are shown only for the app you name.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listArgs) (*mcp.CallToolResult, windowList, error) {
		ws := scr.Windows()
		for i := range ws {
			if in.App == "" || !strings.Contains(strings.ToLower(ws[i].App), strings.ToLower(in.App)) {
				ws[i].Title = "" // other apps' titles can be private (mail subjects, documents)
			}
		}
		return nil, windowList{ws}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "lessons",
		Description: "Read or record lessons for one app: what slowed the work down and what worked. " +
			"Read them before planning in that app. Add one when a task was slow, a failure repeated, or a server hint says the loop is inefficient. " +
			"At the end of a task, call it with done: it records what the task cost under the current lessons, and says when the lessons are due for a review. " +
			"Use app all for rules that hold in every app; every app read also returns them. Without app, it lists the apps that have lessons, tasks or recipes. " +
			"add and done can go in one call. " +
			"Also returns the recent tasks, the cost per lessons version, and this session's counters. Lessons stay on this Mac; never record passwords, personal text or window contents.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in lessonArgs) (*mcp.CallToolResult, lessonReport, error) {
		if in.App == "" {
			if in.Add != "" || in.Set != nil || in.Done != "" {
				return nil, lessonReport{}, fmt.Errorf("give the app name, e.g. Safari")
			}
			apps, err := listApps()
			if err != nil {
				return nil, lessonReport{}, err
			}
			return nil, lessonReport{Apps: apps, Stats: st.snapshot()}, nil
		}
		var lines []string
		var version int
		var err error
		switch {
		case in.Set != nil:
			lines, version, err = setLessons(in.App, in.Set)
		case in.Add != "":
			lines, version, err = addLesson(in.App, in.Add)
		default:
			lines, version, err = readLessons(in.App)
		}
		if err != nil {
			return nil, lessonReport{}, err
		}
		out := lessonReport{App: in.App, Version: version, Lessons: lines}
		if !strings.EqualFold(in.App, globalApp) {
			out.Rules, _, _ = readLessons(globalApp)
		}
		var tasks []task
		if in.Done != "" {
			if len(in.Done) > 120 {
				return nil, lessonReport{}, fmt.Errorf("keep done under 120 characters, and generic")
			}
			t := st.finish(strings.Join(strings.Fields(in.Done), " "), version)
			if tasks, err = addTask(in.App, t); err != nil {
				return nil, lessonReport{}, err
			}
			if !strings.EqualFold(in.App, globalApp) { // the rules for every app are judged by every task
				_, gv, _ := readLessons(globalApp)
				t.Version = gv
				if all, err := addTask(globalApp, t); err == nil {
					if r := review(globalApp, gv, all); r != "" {
						out.Review = r + "\n"
					}
				}
			}
			out.Review = review(in.App, version, tasks) + "\n" + out.Review
		} else {
			if tasks, err = readTasks(in.App); err != nil {
				return nil, lessonReport{}, err
			}
			out.Review = review(in.App, version, tasks) // a review due at the end of an earlier session
		}
		out.Review = strings.TrimSpace(out.Review)
		out.Versions = byVersion(tasks)
		out.Tasks = tasks[max(0, len(tasks)-10):]
		out.Stats = st.snapshot()
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "screenshot",
		Description: "Look at one app's window: name it with app or window_id (a covered window still works). Start low at the default 1024 px. " +
			"Zoom only when a target is small, text is unreadable or two targets are close: pass shot plus region (in that shot's pixels) " +
			"for a sharp crop, or shot plus mark to see exactly where a click would land. " +
			"Each image has a shot id; pass it with any coordinates you read from that image.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in shotArgs) (*mcp.CallToolResult, shotInfo, error) {
		req := shotRequest{Width: in.Width, Mark: in.Mark}
		switch {
		case in.Shot != 0:
			b, err := reg.get(in.Shot)
			if err != nil {
				return nil, shotInfo{}, err
			}
			if in.WindowID != 0 || in.App != "" {
				return nil, shotInfo{}, fmt.Errorf("a zoom stays in shot %d's frame; leave out window_id and app", in.Shot)
			}
			if len(in.Region) != 0 && len(in.Region) != 4 {
				return nil, shotInfo{}, fmt.Errorf("region is [x, y, width, height]")
			}
			req.Base = b
			copy(req.Region[:], in.Region)
		case len(in.Region) != 0 || in.Mark != nil:
			return nil, shotInfo{}, fmt.Errorf("region and mark need shot: the id of the image they refer to")
		case in.WindowID != 0 || in.App != "":
			w, err := scr.FindWindow(in.WindowID, in.App)
			if err != nil {
				return nil, shotInfo{}, err
			}
			req.Window = w.ID
		case !opt.AllowDisplay:
			return nil, shotInfo{}, fmt.Errorf("%s", displayOff)
		}
		sh, img, err := take(ctx, scr, reg, req)
		if err != nil {
			return nil, shotInfo{}, err
		}
		out := info(scr, e, sh)
		note := out.note()
		if req.Base == nil && sh.Window != 0 {
			if rs, _ := readRecipes(sh.App); len(rs) > 0 {
				note += "\nSaved recipes for " + sh.App + " (run one with act: recipe, args, shot " + fmt.Sprint(sh.ID) + "):"
				for _, rc := range rs {
					note += "\n- " + rc.summary()
				}
			}
		}
		if hint := st.shot(req.Base != nil); hint != "" {
			note += "\n" + hint
		}
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.ImageContent{Data: img, MIMEType: "image/jpeg"},
			&mcp.TextContent{Text: note},
		}}, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "act",
		Description: "Run a planned sequence of up to 20 actions in one call, then return a new low-detail shot. " +
			"Plan from one shot: chain every step whose target you can already see (click a field, type, press a key), " +
			"and end the sequence where the next target only appears after the screen changes. " +
			"Use wait_for (change or stable) after steps that open, load or animate something. " +
			"To chain past a change you can predict, put an expect step there (until change, optionally a region where it shows): " +
			"if the screen does not change as predicted, the sequence stops before the next steps. " +
			"recipe runs a saved recipe of the shot's app (see recipes). " +
			"The sequence stops at the first failure, or when the person moves the mouse.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in actArgs) (*mcp.CallToolResult, actReport, error) {
		observe := in.Observe == nil || *in.Observe
		steps := in.Steps
		var app string
		if in.Recipe != "" {
			sh, err := reg.get(in.Shot)
			if err != nil {
				return nil, actReport{}, err
			}
			rc, err := findRecipe(sh.App, in.Recipe)
			if err != nil {
				return nil, actReport{}, err
			}
			pre, err := rc.expand(sh, in.Args)
			if err != nil {
				return nil, actReport{}, err
			}
			steps, app = append(pre, steps...), sh.App
		}
		res, rep, err := do(ctx, steps, in.Shot, observe, in.Width, true, in.Mode, in.MaxAge)
		if err != nil || res == nil {
			return res, rep, err
		}
		hint := ""
		switch {
		case app != "":
			st.predicted(0, 0, true)
			countRun(app, in.Recipe, rep.Error != "")
			if rep.Error != "" {
				hint = "Recipe " + in.Recipe + " failed. If the app changed, fix it with recipes (save, same name) or delete it."
			}
		case rep.Error == "" && actions(steps) >= 3:
			if sh, err := reg.get(in.Shot); err == nil && sh.Window != 0 {
				hint = st.saveHint(sh.App)
			}
		}
		if hint != "" {
			res.Content = append(res.Content, &mcp.TextContent{Text: hint})
		}
		return res, rep, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "recipes",
		Description: "List, save or delete recipes: sequences that worked in an app, saved so the same situation next time is one act call. " +
			"Save right after an act sequence succeeded and will likely recur: pass the same steps and shot, a name, and when (the situation). " +
			"Typed text becomes {{textN}} placeholders filled with args at run time. A recipe runs only on a window of the size it was saved at. " +
			"Screenshots of an app list its recipes. Recipes stay on this Mac.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in recipeArgs) (*mcp.CallToolResult, recipeReport, error) {
		app := in.App
		switch {
		case in.Save != "":
			rc, sh, err := newRecipe(reg, in.Save, in.When, in.Steps, in.Shot, in.KeepText)
			if err != nil {
				return nil, recipeReport{}, err
			}
			rs, err := saveRecipe(sh.App, rc)
			if err != nil {
				return nil, recipeReport{}, err
			}
			return nil, recipeReport{App: sh.App, Recipes: rs}, nil
		case in.Delete != "":
			rs, err := readRecipes(app)
			if err != nil {
				return nil, recipeReport{}, err
			}
			kept := rs[:0]
			for _, r := range rs {
				if r.Name != in.Delete {
					kept = append(kept, r)
				}
			}
			if len(kept) == len(rs) {
				return nil, recipeReport{}, fmt.Errorf("no recipe %q for %s", in.Delete, app)
			}
			return nil, recipeReport{App: app, Recipes: kept}, writeRecipes(app, kept)
		}
		rs, err := readRecipes(app)
		if err != nil {
			return nil, recipeReport{}, err
		}
		return nil, recipeReport{App: app, Recipes: rs}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "cursor_position",
		Description: "Return the cursor position in global points and in a shot's pixels.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in cursorArgs) (*mcp.CallToolResult, cursorInfo, error) {
		p, err := e.Cursor.Position()
		if err != nil {
			return nil, cursorInfo{}, err
		}
		out := cursorInfo{Global: position{p.X, p.Y}}
		sh := reg.latest()
		if in.Shot != 0 {
			if sh, err = reg.get(in.Shot); err != nil {
				return nil, cursorInfo{}, err
			}
		}
		if sh != nil {
			out.Shot = sh.ID
			if px, ok := pixelOf(scr, sh, p); ok {
				out.Pixel = &px
			}
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "mouse_move",
		Description: "Move the mouse to a pixel of a shot along a human-like path.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in moveArgs) (*mcp.CallToolResult, actReport, error) {
		return do(ctx, []step{{Do: "move", X: &in.X, Y: &in.Y, Size: in.Size}}, in.Shot, in.Observe, 0, false, in.Mode, 0)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "click",
		Description: "Click, optionally moving first to a pixel of a shot. For several actions in a row, use act.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in clickArgs) (*mcp.CallToolResult, actReport, error) {
		st := step{Do: "click", X: in.X, Y: in.Y, Size: in.Size}
		switch {
		case in.Button == "right":
			st.Do = "right_click"
		case in.Button != "" && in.Button != "left":
			return nil, actReport{}, fmt.Errorf("button is left or right")
		case in.Count == 2:
			st.Do = "double_click"
		}
		return do(ctx, []step{st}, in.Shot, in.Observe, 0, false, in.Mode, 0)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "drag",
		Description: "Hold the left button at the current position, move to a pixel of a shot and release. Move to the start first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dragArgs) (*mcp.CallToolResult, actReport, error) {
		return do(ctx, []step{{Do: "drag", X: &in.X, Y: &in.Y}}, in.Shot, in.Observe, 0, false, in.Mode, 0)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "scroll",
		Description: "Turn the scroll wheel at the current cursor position.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in scrollArgs) (*mcp.CallToolResult, actReport, error) {
		return do(ctx, []step{{Do: "scroll", DX: in.DX, DY: in.DY}}, 0, in.Observe, 0, false, in.Mode, 0)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "type_text",
		Description: "Type text with human rhythm into the focused field.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in typeArgs) (*mcp.CallToolResult, actReport, error) {
		return do(ctx, []step{{Do: "type", Text: in.Text, WPM: in.WPM}}, 0, in.Observe, 0, false, in.Mode, 0)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "key",
		Description: "Press a key or key combination such as enter, escape, cmd+c, cmd+shift+4.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in keyArgs) (*mcp.CallToolResult, actReport, error) {
		return do(ctx, []step{{Do: "key", Keys: in.Keys}}, 0, in.Observe, 0, false, in.Mode, 0)
	})

	return s
}

// actions counts the steps that act on the app, not waits or checks.
func actions(steps []step) int {
	n := 0
	for _, s := range steps {
		switch s.Do {
		case "wait", "wait_for", "expect":
		default:
			n++
		}
	}
	return n
}
