package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Lucklyric/virtual-nanokvm/internal/hid"
	"github.com/Lucklyric/virtual-nanokvm/internal/human"
	"github.com/Lucklyric/virtual-nanokvm/internal/screen"
)

func TestLessonsAddDedupeAndCap(t *testing.T) {
	t.Setenv("VNKVM_LESSONS_DIR", t.TempDir())
	if _, err := addLesson("Safari", "Use cmd+l to reach the address bar"); err != nil {
		t.Fatal(err)
	}
	lines, _ := addLesson("safari", "use cmd+L to reach the address bar") // same app, same lesson
	if len(lines) != 1 {
		t.Fatalf("duplicate kept: %v", lines)
	}
	for i := 0; i < maxLessons+5; i++ {
		addLesson("Safari", fmt.Sprintf("lesson %d", i))
	}
	lines, _ = readLessons("Safari")
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
