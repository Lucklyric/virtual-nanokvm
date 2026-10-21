// Package web serves a browser viewer on the local network: a live MJPEG
// stream of the main display, and remote control of the mouse and keyboard.
//
// Two tokens set two roles. A view token can watch. A control token can also
// drive input. Only one controller is connected at a time. The person's own
// hand movement is relayed as-is (it is already human), through whichever
// input path the service runs (sys or driver).
package web

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/Lucklyric/virtual-nanokvm/internal/human"
	"github.com/Lucklyric/virtual-nanokvm/internal/keymap"
)

//go:embed viewer.html
var viewerHTML []byte

// Controller takes relayed input. *human.Engine implements it.
type Controller interface {
	RelayMove(p human.Point) error
	RelayButtons(buttons uint32) error
	RelayWheel(dx, dy int) error
	RelayKeys(modifiers uint8, usages []uint16) error
	ReleaseAll() error
}

// Source captures frames of the main display.
type Source interface {
	Frame(width int, quality float64) ([]byte, error)
	Bounds() (width, height float64) // in points
}

// Config sets tokens and stream quality.
type Config struct {
	ControlToken string
	ViewToken    string
	FPS          int
	Width        int     // stream width in pixels
	Quality      float64 // JPEG quality, 0..1
}

// Server is the viewer, stream and control endpoint.
type Server struct {
	cfg        Config
	ctl        Controller
	src        Source
	controlled atomic.Bool

	mu   sync.Mutex
	subs map[chan []byte]struct{}
	wake chan struct{}
}

// New returns a Server. Call Run to start capturing.
func New(cfg Config, ctl Controller, src Source) *Server {
	if cfg.FPS <= 0 {
		cfg.FPS = 10
	}
	if cfg.Width <= 0 {
		cfg.Width = 1280
	}
	if cfg.Quality <= 0 {
		cfg.Quality = 0.6
	}
	return &Server{cfg: cfg, ctl: ctl, src: src, subs: map[chan []byte]struct{}{}, wake: make(chan struct{}, 1)}
}

// Handler routes the viewer page, the stream and the control socket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(viewerHTML)
	})
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /stream", s.stream)
	mux.HandleFunc("GET /control", s.control)
	return mux
}

func (s *Server) role(r *http.Request) string {
	t := []byte(r.URL.Query().Get("token"))
	switch {
	case len(t) == 0:
		return ""
	case subtle.ConstantTimeCompare(t, []byte(s.cfg.ControlToken)) == 1:
		return "control"
	case subtle.ConstantTimeCompare(t, []byte(s.cfg.ViewToken)) == 1:
		return "view"
	}
	return ""
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	role := s.role(r)
	if role == "" {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	width, height := s.src.Bounds()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"role": role, "width": width, "height": height, "fps": s.cfg.FPS,
		"controlled": s.controlled.Load(),
	})
}

// Run captures frames while anyone watches, until ctx ends.
func (s *Server) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second / time.Duration(s.cfg.FPS))
	defer tick.Stop()
	for {
		s.mu.Lock()
		watching := len(s.subs) > 0
		s.mu.Unlock()
		if !watching {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		frame, err := s.src.Frame(s.cfg.Width, s.cfg.Quality)
		if err != nil {
			log.Print(err)
			continue
		}
		s.mu.Lock()
		for ch := range s.subs {
			select { // keep only the newest frame for a slow viewer
			case <-ch:
			default:
			}
			ch <- frame
		}
		s.mu.Unlock()
	}
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if s.role(r) == "" {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	ch := make(chan []byte, 1)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	select {
	case s.wake <- struct{}{}:
	default:
	}

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	for {
		select {
		case <-r.Context().Done():
			return
		case frame := <-ch:
			if _, err := fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame)); err != nil {
				return
			}
			if _, err := w.Write(append(frame, '\r', '\n')); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// event is one input message from the viewer. Positions are fractions of the
// stream image (0..1), so the viewer needs no knowledge of display size.
type event struct {
	T       string  `json:"t"` // move, buttons, wheel, key, release
	X       float64 `json:"x,omitempty"`
	Y       float64 `json:"y,omitempty"`
	Buttons uint32  `json:"b,omitempty"`
	DX      int     `json:"dx,omitempty"`
	DY      int     `json:"dy,omitempty"`
	Code    string  `json:"code,omitempty"`
	Down    bool    `json:"down,omitempty"`
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	if s.role(r) != "control" {
		http.Error(w, "a control token is required", http.StatusForbidden)
		return
	}
	if !s.controlled.CompareAndSwap(false, true) {
		http.Error(w, "someone else is in control", http.StatusConflict)
		return
	}
	defer s.controlled.Store(false)
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	// Whatever ends the session, nothing stays held down.
	defer s.ctl.ReleaseAll()

	kb := keyboard{}
	for {
		var ev event
		if err := wsjson.Read(r.Context(), conn, &ev); err != nil {
			if !errors.Is(err, context.Canceled) && websocket.CloseStatus(err) == -1 {
				log.Print("control: ", err)
			}
			return
		}
		if err := s.apply(ev, &kb); err != nil {
			log.Print("control: ", err)
		}
	}
}

func (s *Server) apply(ev event, kb *keyboard) error {
	switch ev.T {
	case "move":
		w, h := s.src.Bounds()
		x := min(max(ev.X, 0), 1) * (w - 1)
		y := min(max(ev.Y, 0), 1) * (h - 1)
		return s.ctl.RelayMove(human.Point{X: x, Y: y})
	case "buttons":
		return s.ctl.RelayButtons(ev.Buttons & 0x7)
	case "wheel":
		return s.ctl.RelayWheel(max(-20, min(20, ev.DX)), max(-20, min(20, ev.DY)))
	case "key":
		if !kb.set(ev.Code, ev.Down) {
			return nil // a key the US layout has no usage for
		}
		return s.ctl.RelayKeys(kb.mods, kb.keys)
	case "release":
		*kb = keyboard{}
		return s.ctl.ReleaseAll()
	}
	return fmt.Errorf("unknown event %q", ev.T)
}

// keyboard tracks what the remote keyboard holds, as a boot report would.
type keyboard struct {
	mods uint8
	keys []uint16
}

func (k *keyboard) set(code string, down bool) bool {
	usage, mod, ok := keymap.Code(code)
	if !ok {
		return false
	}
	if mod != 0 {
		if down {
			k.mods |= mod
		} else {
			k.mods &^= mod
		}
		return true
	}
	for i, u := range k.keys {
		if u == usage {
			if !down {
				k.keys = append(k.keys[:i], k.keys[i+1:]...)
			}
			return true
		}
	}
	if down && len(k.keys) < 6 {
		k.keys = append(k.keys, usage)
	}
	return true
}
