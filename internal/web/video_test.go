package web

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeVideo emits a keyframe every 4th frame, like a short GOP.
type fakeVideo struct {
	mu    sync.Mutex
	n     int
	out   []VideoChunk
	keys  int
	stops int
}

func (f *fakeVideo) EncodeVideo(width, fps, kbps int, pts int64, key bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	k := key || f.n%4 == 1
	if key {
		f.keys++
	}
	f.out = append(f.out, VideoChunk{Data: []byte{0, 0, 0, 1, byte(f.n)}, Key: k, PTS: pts})
	return nil
}

func (f *fakeVideo) NextVideo(timeoutMS int) (VideoChunk, bool) {
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	for {
		f.mu.Lock()
		if len(f.out) > 0 {
			c := f.out[0]
			f.out = f.out[1:]
			f.mu.Unlock()
			return c, true
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			return VideoChunk{}, false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (f *fakeVideo) VideoConfig() (VideoConfig, int) {
	return VideoConfig{Codec: "avc1.4d0028", Description: []byte{1, 0x4d, 0, 0x28}, Width: 1280, Height: 720}, 1
}

func (f *fakeVideo) StopVideo() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
}

func TestVideoStartsWithConfigThenKeyframe(t *testing.T) {
	srv, _ := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, resp, err := websocket.Dial(ctx, "ws"+srv.URL[4:]+"/video?token=bad", nil); err == nil || resp.StatusCode != 401 {
		t.Fatalf("bad token accepted: %v", err)
	}
	c, _, err := websocket.Dial(ctx, "ws"+srv.URL[4:]+"/video?token=view", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()

	typ, data, err := c.Read(ctx)
	if err != nil || typ != websocket.MessageText {
		t.Fatalf("first message: %v %v", typ, err)
	}
	var cfg struct {
		T      string      `json:"t"`
		Config VideoConfig `json:"config"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.T != "config" || cfg.Config.Codec != "avc1.4d0028" {
		t.Fatalf("config message %s: %v", data, err)
	}
	var last uint64
	for i := range 8 {
		typ, data, err := c.Read(ctx)
		if err != nil || typ != websocket.MessageBinary || len(data) < 9 {
			t.Fatalf("frame %d: %v %v", i, typ, err)
		}
		if i == 0 && data[0] != 1 {
			t.Fatal("first frame after the config is not a keyframe")
		}
		pts := binary.BigEndian.Uint64(data[1:9])
		if i > 0 && pts <= last {
			t.Fatalf("frame %d: timestamps not increasing", i)
		}
		last = pts
	}
}
