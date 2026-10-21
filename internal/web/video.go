package web

import (
	"context"
	"encoding/binary"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// VideoChunk is one encoded H.264 frame in AVCC form.
type VideoChunk struct {
	Data []byte
	Key  bool
	PTS  int64 // microseconds
}

// VideoConfig is what a browser decoder needs: codec string and avcC record.
type VideoConfig struct {
	Codec       string `json:"codec"`
	Description []byte `json:"description"` // base64 in JSON
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// VideoSource encodes the main display to H.264. Encoding is asynchronous:
// EncodeVideo submits a frame and NextVideo returns encoder output.
type VideoSource interface {
	EncodeVideo(width, fps, kbps int, pts int64, key bool) error
	NextVideo(timeoutMS int) (VideoChunk, bool)
	VideoConfig() (VideoConfig, int)
	StopVideo()
}

// videoSub is one /video viewer. It receives the config whenever it changes
// and skips frames until a keyframe, since a decoder cannot start mid-GOP.
type videoSub struct {
	ch      chan wsMsg
	seq     int
	needKey bool
}

type wsMsg struct {
	text bool
	data []byte
}

// RunVideo encodes while anyone watches over /video, until ctx ends.
func (s *Server) RunVideo(ctx context.Context) {
	if s.video == nil {
		return
	}
	for {
		s.mu.Lock()
		watching := len(s.vsubs) > 0
		s.mu.Unlock()
		if !watching {
			select {
			case <-ctx.Done():
				return
			case <-s.vwake:
			}
			continue
		}
		s.encodeWhileWatched(ctx)
	}
}

func (s *Server) encodeWhileWatched(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer s.video.StopVideo()

	// The encoder hands frames back asynchronously; fan them out as they come.
	go func() {
		for ctx.Err() == nil {
			if c, ok := s.video.NextVideo(200); ok {
				s.fanOut(c)
			}
		}
	}()

	start := time.Now()
	tick := time.NewTicker(time.Second / time.Duration(s.cfg.FPS))
	defer tick.Stop()
	lastErr := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		s.mu.Lock()
		watching := len(s.vsubs) > 0
		s.mu.Unlock()
		if !watching {
			return
		}
		key := s.keyWanted.Swap(false)
		if err := s.video.EncodeVideo(s.cfg.Width, s.cfg.FPS, s.cfg.Kbps, time.Since(start).Microseconds(), key); err != nil {
			if key {
				s.keyWanted.Store(true) // try again next frame
			}
			if time.Since(lastErr) > 10*time.Second {
				log.Print(err)
				lastErr = time.Now()
			}
		}
	}
}

func (s *Server) fanOut(c VideoChunk) {
	cfg, seq := s.video.VideoConfig()
	frame := make([]byte, 9+len(c.Data))
	if c.Key {
		frame[0] = 1
	}
	binary.BigEndian.PutUint64(frame[1:9], uint64(c.PTS))
	copy(frame[9:], c.Data)

	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.vsubs {
		if sub.seq != seq && seq != 0 {
			if msg, err := jsonMsg(map[string]any{"t": "config", "config": cfg}); err == nil {
				select {
				case sub.ch <- msg:
					sub.seq = seq
					sub.needKey = true
				default:
				}
			}
		}
		if sub.seq != seq || (sub.needKey && !c.Key) {
			continue
		}
		select {
		case sub.ch <- wsMsg{data: frame}:
			sub.needKey = false
		default:
			// The viewer fell behind: resume it from the next keyframe.
			sub.needKey = true
			s.keyWanted.Store(true)
		}
	}
}

func (s *Server) videoWS(w http.ResponseWriter, r *http.Request) {
	if s.role(r) == "" {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if s.video == nil {
		http.Error(w, "H.264 is not available", http.StatusNotFound)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context()) // the viewer sends nothing; notice when it leaves

	sub := &videoSub{ch: make(chan wsMsg, 30), needKey: true}
	s.mu.Lock()
	s.vsubs[sub] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.vsubs, sub)
		s.mu.Unlock()
	}()
	s.keyWanted.Store(true)
	select {
	case s.vwake <- struct{}{}:
	default:
	}

	for {
		select {
		case <-ctx.Done():
			return
		case m := <-sub.ch:
			typ := websocket.MessageBinary
			if m.text {
				typ = websocket.MessageText
			}
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(wctx, typ, m.data)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
