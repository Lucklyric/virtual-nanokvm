package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Lucklyric/virtual-nanokvm/internal/hid"
	"github.com/Lucklyric/virtual-nanokvm/internal/human"
	"github.com/Lucklyric/virtual-nanokvm/internal/screen"
)

func TestLessonsAddDedupeAndCap(t *testing.T) {
	t.Setenv("VNKVM_LESSONS_DIR", t.TempDir())
	if _, _, err := addLesson("Safari", "Use cmd+l to reach the address bar"); err != nil {
		t.Fatal(err)
	}
	lines, v, _ := addLesson("safari", "use cmd+L to reach the address bar") // same app, same lesson
	if len(lines) != 1 || v != 0 || !strings.HasPrefix(lines[0], "v0 ") {
		t.Fatalf("duplicate kept or untagged: v%d %v", v, lines)
	}
	for i := 0; i < maxLessons+5; i++ {
		addLesson("Safari", fmt.Sprintf("lesson %d", i))
	}
	lines, _, _ = readLessons("Safari")
	if len(lines) != maxLessons || !strings.HasSuffix(lines[len(lines)-1], fmt.Sprintf("lesson %d", maxLessons+4)) {
		t.Fatalf("kept %d lessons, last %q", len(lines), lines[len(lines)-1])
	}
	if _, err := lessonsFile("../.."); err == nil {
		t.Fatal("a path-like app name was accepted")
	}
}

func TestStatsHints(t *testing.T) {
	s := &stats{}
	s.shot(false)
	s.shot(true)
	if h := s.shot(true); !strings.Contains(h, "3 screenshots") {
		t.Fatalf("no hint after 3 looks: %q", h)
	}
	s.action(true, 1, nil)
	s.action(true, 1, nil)
	if h := s.action(true, 1, nil); !strings.Contains(h, "act call") {
		t.Fatalf("no hint after 3 single actions: %q", h)
	}
	s.action(false, 2, errors.New("window 7 is covered at that point"))
	if h := s.action(false, 1, errors.New("window 7 is covered at that point")); !strings.Contains(h, "same failure (covered)") {
		t.Fatalf("no hint after a repeated failure: %q", h)
	}
	if got := s.snapshot(); got.Shots != 3 || got.Zooms != 2 || got.Singles != 3 || got.Acts != 2 || got.Failures["covered"] != 2 {
		t.Fatalf("counters %+v", got)
	}
}

// fixedCursor stays where it is put, enough for tools that only read it.
type fixedCursor struct{}

func (fixedCursor) Position() (human.Point, error) { return human.Point{X: 10, Y: 10}, nil }

func connect(t *testing.T, scr Screen, opt Options) *mcp.ClientSession {
	t.Helper()
	e := human.New(&hid.Recorder{}, fixedCursor{})
	srv := New(e, scr, opt)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestScreenshotsStayInOneApp(t *testing.T) {
	scr := &fakeScreen{windows: []screen.Window{
		{ID: 7, App: "Notes", Title: "Groceries", X: 100, Y: 80, Width: 800, Height: 600},
		{ID: 9, App: "Mail", Title: "Re: private subject", X: 0, Y: 0, Width: 400, Height: 400},
	}}
	cs := connect(t, scr, Options{})

	if res := callTool(t, cs, "screenshot", nil); !res.IsError || !strings.Contains(text(res), "whole-display screenshots are off") {
		t.Fatalf("whole display captured without permission: %s", text(res))
	}
	if res := callTool(t, cs, "screenshot", map[string]any{"app": "Notes"}); res.IsError {
		t.Fatalf("app screenshot failed: %s", text(res))
	}
	// Other apps' titles stay hidden unless that app is named.
	res := callTool(t, cs, "list_windows", map[string]any{"app": "Notes"})
	if s := fmt.Sprint(res.StructuredContent); strings.Contains(s, "private subject") || !strings.Contains(s, "Groceries") {
		t.Fatalf("window titles: %s", s)
	}

	open := connect(t, scr, Options{AllowDisplay: true})
	if res := callTool(t, open, "screenshot", nil); res.IsError {
		t.Fatalf("display screenshot refused with AllowDisplay: %s", text(res))
	}
}

type fakeBackground struct{ calls []string }

func (f *fakeBackground) Act(_ context.Context, pid int, x, y float64, menu bool) (string, error) {
	f.calls = append(f.calls, fmt.Sprintf("act %d %.0f %.0f %v", pid, x, y, menu))
	return "pressed AXButton", nil
}
func (f *fakeBackground) Menu(pid int, path string) error {
	f.calls = append(f.calls, fmt.Sprintf("menu %d %s", pid, path))
	return nil
}
func (f *fakeBackground) Engine(int, float64, float64) *human.Engine {
	return human.New(&hid.Recorder{}, fixedCursor{})
}

func TestBackgroundModeStaysInTheApp(t *testing.T) {
	scr := &fakeScreen{windows: []screen.Window{{ID: 7, PID: 42, App: "Notes", X: 100, Y: 80, Width: 800, Height: 600}}}
	bg := &fakeBackground{}
	cs := connect(t, scr, Options{Background: bg})
	callTool(t, cs, "screenshot", map[string]any{"app": "Notes"})

	res := callTool(t, cs, "act", map[string]any{"shot": 1, "mode": "background", "observe": false, "steps": []map[string]any{
		{"do": "click", "x": 512, "y": 384},
		{"do": "menu", "menu": "File > New"},
		{"do": "move", "x": 10, "y": 10},
	}})
	if res.IsError {
		t.Fatalf("background act failed: %s", text(res))
	}
	// Pixel centers: (512.5, 384.5) × 0.78125 pt/px, plus the window origin (100, 80).
	if len(bg.calls) != 2 || bg.calls[0] != "act 42 500 380 false" || bg.calls[1] != "menu 42 File > New" {
		t.Fatalf("background calls %v", bg.calls)
	}
	if !strings.Contains(text(res), "no cursor") {
		t.Fatalf("move in background was not reported as skipped: %s", text(res))
	}

	for _, do := range []string{"right_click", "double_click", "drag"} {
		res := callTool(t, cs, "act", map[string]any{"shot": 1, "mode": "background", "steps": []map[string]any{{"do": do, "x": 5, "y": 5}}})
		if !res.IsError {
			t.Errorf("%s ran in background mode", do)
		}
	}
	if res := callTool(t, cs, "act", map[string]any{"mode": "sideways", "steps": []map[string]any{{"do": "key", "keys": "a"}}}); !res.IsError {
		t.Error("unknown mode accepted")
	}
}

func TestSetKeepsTagsAndBumpsVersion(t *testing.T) {
	t.Setenv("VNKVM_LESSONS_DIR", t.TempDir())
	addLesson("Notes", "first")
	lines, _, _ := addLesson("Notes", "second")
	lines, v, err := setLessons("Notes", []string{lines[0], "merged rule"})
	if err != nil || v != 1 || !strings.HasPrefix(lines[0], "v0 ") || !strings.HasPrefix(lines[1], "v1 ") {
		t.Fatalf("set: v%d %v %v", v, lines, err)
	}
	if lines, v, _ = addLesson("Notes", "between revisions"); v != 1 || !strings.HasPrefix(lines[2], "v1 ") {
		t.Fatalf("add changed the version or tag: v%d %v", v, lines)
	}
	if _, v, _ := readLessons("Notes"); v != 1 {
		t.Fatalf("version read back as %d", v)
	}
}

func TestDoneRecordsTasksAndAsksForReview(t *testing.T) {
	t.Setenv("VNKVM_LESSONS_DIR", t.TempDir())
	scr := &fakeScreen{windows: []screen.Window{{ID: 7, PID: 42, App: "Notes", X: 100, Y: 80, Width: 800, Height: 600}}}
	cs := connect(t, scr, Options{})
	callTool(t, cs, "lessons", map[string]any{"app": "Notes", "add": "use cmd+f"})
	callTool(t, cs, "lessons", map[string]any{"app": "Notes", "set": []string{"use cmd+f"}}) // v1
	callTool(t, cs, "lessons", map[string]any{"app": "all", "add": "prefer the keyboard"})

	var last *mcp.CallToolResult
	for i := 0; i < 5; i++ {
		callTool(t, cs, "screenshot", map[string]any{"app": "Notes"})
		last = callTool(t, cs, "lessons", map[string]any{"app": "Notes", "done": "find a note"})
	}
	out := fmt.Sprint(last.StructuredContent)
	// Each task: one screenshot plus the lessons call itself.
	if !strings.Contains(out, "tool_calls:2") || !strings.Contains(out, "lessons_version:1") {
		t.Fatalf("task record: %s", out)
	}
	if !strings.Contains(out, "5 tasks since v1") {
		t.Fatalf("no review after 5 tasks: %s", out)
	}
	// A new session still sees the review that fell due.
	if s := fmt.Sprint(callTool(t, connect(t, scr, Options{}), "lessons", map[string]any{"app": "Notes"}).StructuredContent); !strings.Contains(s, "5 tasks since v1") {
		t.Fatalf("review lost on read: %s", s)
	}
	if all, _ := readTasks(globalApp); len(all) != 5 {
		t.Fatalf("global rules saw %d tasks", len(all))
	}
	if !strings.Contains(out, "prefer the keyboard") {
		t.Fatalf("app read lacks the all rules: %s", out)
	}
	list := fmt.Sprint(callTool(t, cs, "lessons", nil).StructuredContent)
	if !strings.Contains(list, "app:notes") || !strings.Contains(list, "review_due:true") || !strings.Contains(list, "app:all") {
		t.Fatalf("app list: %s", list)
	}
}

func TestReviewFlagsAWorseVersion(t *testing.T) {
	var tasks []task
	for i := 0; i < 3; i++ {
		tasks = append(tasks, task{Version: 1, Calls: 4}, task{Version: 2, Calls: 9})
	}
	if r := review("Notes", 2, tasks); !strings.Contains(r, "v1 took 4.0") {
		t.Fatalf("regression not flagged: %q", r)
	}
	if r := review("Notes", 2, tasks[:4]); r != "" {
		t.Fatalf("flagged on too few tasks: %q", r)
	}
}

func TestRecipeFollowsTheWindowAndFillsText(t *testing.T) {
	t.Setenv("VNKVM_LESSONS_DIR", t.TempDir())
	scr := &fakeScreen{windows: []screen.Window{{ID: 7, PID: 42, App: "Notes", X: 100, Y: 80, Width: 800, Height: 600}}}
	bg := &fakeBackground{}
	cs := connect(t, scr, Options{Background: bg})
	callTool(t, cs, "screenshot", map[string]any{"app": "Notes"})

	steps := []map[string]any{
		{"do": "click", "x": 512, "y": 384},
		{"do": "type", "text": "secret groceries"},
		{"do": "key", "keys": "enter"},
	}
	res := callTool(t, cs, "recipes", map[string]any{"save": "find_note", "when": "Notes window, list visible", "shot": 1, "steps": steps})
	if res.IsError {
		t.Fatalf("save: %s", text(res))
	}
	if s := fmt.Sprint(res.StructuredContent); strings.Contains(s, "secret") || !strings.Contains(s, "text2") {
		t.Fatalf("typed text was stored: %s", s)
	}

	scr.windows[0].X, scr.windows[0].Y = 300, 200 // the window moved
	shot := callTool(t, cs, "screenshot", map[string]any{"app": "Notes"})
	if !strings.Contains(text(shot), "find_note(text2): Notes window") {
		t.Fatalf("screenshot does not list the recipe: %s", text(shot))
	}
	res = callTool(t, cs, "act", map[string]any{"shot": 2, "mode": "background", "observe": false, "recipe": "find_note"})
	if !res.IsError || !strings.Contains(text(res), "needs args: text2") {
		t.Fatalf("missing args accepted: %s", text(res))
	}
	res = callTool(t, cs, "act", map[string]any{"shot": 2, "mode": "background", "observe": false, "recipe": "find_note", "args": map[string]any{"text2": "x"}})
	if res.IsError {
		t.Fatalf("recipe run: %s", text(res))
	}
	if len(bg.calls) != 1 || bg.calls[0] != "act 42 700 500 false" {
		t.Fatalf("recipe click did not follow the window: %v", bg.calls)
	}
	if rs, _ := readRecipes("Notes"); rs[0].Runs != 1 {
		t.Fatalf("run not counted: %+v", rs[0])
	}

	scr.windows[0].Width = 900 // resized: positions moved
	callTool(t, cs, "screenshot", map[string]any{"app": "Notes"})
	if res := callTool(t, cs, "act", map[string]any{"shot": 3, "mode": "background", "recipe": "find_note", "args": map[string]any{"text2": "x"}}); !res.IsError {
		t.Fatal("recipe ran on a resized window")
	}
}

func TestExpectMissStopsTheSequence(t *testing.T) {
	scr := &fakeScreen{windows: []screen.Window{{ID: 7, PID: 42, App: "Notes", X: 100, Y: 80, Width: 800, Height: 600}}}
	bg := &fakeBackground{}
	cs := connect(t, scr, Options{Background: bg})
	callTool(t, cs, "screenshot", map[string]any{"app": "Notes"})
	// The fake window never changes, so the predicted change does not come.
	res := callTool(t, cs, "act", map[string]any{"shot": 1, "mode": "background", "observe": false, "steps": []map[string]any{
		{"do": "click", "x": 10, "y": 10},
		{"do": "expect", "until": "change", "region": []float64{0, 0, 100, 50}, "ms": 300},
		{"do": "click", "x": 20, "y": 20},
	}})
	if !res.IsError || !strings.Contains(text(res), "prediction missed") || len(bg.calls) != 1 {
		t.Fatalf("expect did not stop the sequence: %s %v", text(res), bg.calls)
	}
	if res := callTool(t, cs, "act", map[string]any{"shot": 1, "mode": "background", "steps": []map[string]any{{"do": "expect", "until": "change"}}}); !res.IsError {
		t.Fatal("expect change accepted with no step before it")
	}
	out := callTool(t, cs, "lessons", map[string]any{"app": "Notes"})
	if s := fmt.Sprint(out.StructuredContent); !strings.Contains(s, "expect_misses:1") {
		t.Fatalf("miss not counted: %s", s)
	}
}

func TestWaitsWatchAWindowOnAnotherDisplay(t *testing.T) {
	// A window left of the main display: the waits watch the window itself,
	// so they time out on a still window instead of failing to watch.
	scr := &fakeScreen{windows: []screen.Window{{ID: 7, PID: 42, App: "Game", X: -2029, Y: 442, Width: 800, Height: 600}}}
	cs := connect(t, scr, Options{})
	callTool(t, cs, "screenshot", map[string]any{"app": "Game"})
	res := callTool(t, cs, "act", map[string]any{"shot": 1, "observe": false, "steps": []map[string]any{
		{"do": "key", "keys": "a"},
		{"do": "expect", "until": "change", "ms": 300},
	}})
	if s := text(res); !strings.Contains(s, "timed out: no change") || strings.Contains(s, "cannot watch") {
		t.Fatalf("window off the main display: %s", s)
	}
	scr.windows = nil // closed
	res = callTool(t, cs, "act", map[string]any{"shot": 1, "observe": false, "steps": []map[string]any{
		{"do": "key", "keys": "a"},
		{"do": "expect", "until": "change", "ms": 300},
	}})
	if s := text(res); !strings.Contains(s, "window 7 is gone") || !strings.Contains(s, "not checked") {
		t.Fatalf("closed window: %s", s)
	}
}

func TestMaxAgeRefusesAnOldShot(t *testing.T) {
	scr := &fakeScreen{windows: []screen.Window{{ID: 7, PID: 42, App: "Notes", X: 100, Y: 80, Width: 800, Height: 600}}}
	bg := &fakeBackground{}
	cs := connect(t, scr, Options{Background: bg})
	callTool(t, cs, "screenshot", map[string]any{"app": "Notes"})
	time.Sleep(30 * time.Millisecond)
	res := callTool(t, cs, "act", map[string]any{"shot": 1, "mode": "background", "max_age": 10, "steps": []map[string]any{{"do": "click", "x": 5, "y": 5}}})
	if !res.IsError || !strings.Contains(text(res), "stale observation") || len(bg.calls) != 0 {
		t.Fatalf("old shot used: %s", text(res))
	}
	if res := callTool(t, cs, "act", map[string]any{"shot": 1, "mode": "background", "observe": false, "steps": []map[string]any{{"do": "click", "x": 5, "y": 5}}}); res.IsError {
		t.Fatalf("without max_age: %s", text(res))
	}
}
