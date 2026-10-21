package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/Lucklyric/virtual-nanokvm/internal/human"
)

type fakeSource struct{}

func (fakeSource) Frame(int, float64) ([]byte, error) { return []byte("JPEG"), nil }
func (fakeSource) Bounds() (float64, float64)         { return 1001, 501 }

type fakeCtl struct {
	mu       sync.Mutex
	calls    []string
	released chan struct{}
}

func (f *fakeCtl) log(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}
func (f *fakeCtl) RelayMove(p human.Point) error {
	f.log("move " + ftoa(p.X) + " " + ftoa(p.Y))
	return nil
}
func (f *fakeCtl) RelayButtons(b uint32) error { f.log("buttons " + ftoa(float64(b))); return nil }
func (f *fakeCtl) RelayWheel(dx, dy int) error { f.log("wheel"); return nil }
func (f *fakeCtl) RelayKeys(m uint8, u []uint16) error {
	s := "keys " + ftoa(float64(m))
	for _, k := range u {
		s += " " + ftoa(float64(k))
	}
	f.log(s)
	return nil
}
func (f *fakeCtl) ReleaseAll() error {
	f.log("release")
	select {
	case f.released <- struct{}{}:
	default:
	}
	return nil
}

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func setup(t *testing.T) (*httptest.Server, *fakeCtl) {
	t.Helper()
	ctl := &fakeCtl{released: make(chan struct{}, 4)}
	s := New(Config{ControlToken: "ctl", ViewToken: "view", FPS: 50}, ctl, fakeSource{}, &fakeVideo{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.Run(ctx)
	go s.RunVideo(ctx)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, ctl
}

func TestStreamNeedsToken(t *testing.T) {
	srv, _ := setup(t)
	for token, want := range map[string]int{"": 401, "wrong": 401} {
		resp, err := http.Get(srv.URL + "/stream?token=" + token)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("token %q: status %d, want %d", token, resp.StatusCode, want)
		}
	}
	resp, err := http.Get(srv.URL + "/stream?token=view")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if resp.StatusCode != 200 || !strings.HasPrefix(line, "--frame") {
		t.Fatalf("view stream: status %d, first line %q", resp.StatusCode, line)
	}
}

func TestViewTokenCannotControl(t *testing.T) {
	srv, _ := setup(t)
	_, resp, err := websocket.Dial(context.Background(), "ws"+srv.URL[4:]+"/control?token=view", nil)
	if err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("view token got control: err %v", err)
	}
}

func TestControlRelaysInput(t *testing.T) {
	srv, ctl := setup(t)
	ctx := context.Background()
	url := "ws" + srv.URL[4:] + "/control?token=ctl"
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A second controller is turned away while the first is connected.
	if _, resp, err := websocket.Dial(ctx, url, nil); err == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("second controller: err %v", err)
	}
	for _, ev := range []event{
		{T: "move", X: 0.5, Y: 1},
		{T: "buttons", Buttons: 1},
		{T: "key", Code: "ShiftLeft", Down: true},
		{T: "key", Code: "KeyA", Down: true},
		{T: "key", Code: "KeyA"},
	} {
		if err := wsjson.Write(ctx, c, ev); err != nil {
			t.Fatal(err)
		}
	}
	c.Close(websocket.StatusNormalClosure, "")
	select {
	case <-ctl.released:
	case <-time.After(2 * time.Second):
		t.Fatal("keys and buttons were not released when the controller left")
	}
	ctl.mu.Lock()
	defer ctl.mu.Unlock()
	got := strings.Join(ctl.calls, "|")
	// 0.5 of (1001-1) points is 500; 1.0 of (501-1) is 500.
	want := "move 500 500|buttons 1|keys 2|keys 2 4|keys 2|release"
	if got != want {
		t.Fatalf("calls\n got %s\nwant %s", got, want)
	}
}
