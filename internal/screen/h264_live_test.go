package screen

import (
	"encoding/binary"
	"os"
	"testing"
	"time"
)

// VNKVM_LIVE=1 go test ./internal/screen -run H264 -v
// needs Screen Recording permission for the terminal.
func TestH264Live(t *testing.T) {
	if os.Getenv("VNKVM_LIVE") == "" {
		t.Skip("set VNKVM_LIVE=1 to capture and encode the real screen")
	}
	defer H264Stop()
	const fps, n = 15, 45
	start := time.Now()
	var chunks []H264Chunk
	for i := range n {
		pts := time.Since(start).Microseconds()
		if err := H264Encode(1280, fps, 3000, pts, i == 0); err != nil {
			t.Fatal(err)
		}
		for {
			c, ok := H264Next(0)
			if !ok {
				break
			}
			chunks = append(chunks, c)
		}
		time.Sleep(time.Second / fps)
	}
	for {
		c, ok := H264Next(300)
		if !ok {
			break
		}
		chunks = append(chunks, c)
	}
	if len(chunks) < n-2 || !chunks[0].Key {
		t.Fatalf("%d chunks, first key=%v", len(chunks), len(chunks) > 0 && chunks[0].Key)
	}
	total, keys := 0, 0
	for i, c := range chunks {
		// Every chunk is AVCC: 4-byte big-endian lengths that tile the data.
		for off := 0; off < len(c.Data); {
			l := int(binary.BigEndian.Uint32(c.Data[off:]))
			off += 4 + l
			if off > len(c.Data) {
				t.Fatalf("chunk %d: NAL length runs past the data", i)
			}
		}
		if i > 0 && c.PTS <= chunks[i-1].PTS {
			t.Fatalf("chunk %d: timestamps not increasing", i)
		}
		if c.Key {
			keys++
		}
		total += len(c.Data)
	}
	cfg, seq := H264CurrentConfig()
	if seq == 0 || len(cfg.Description) < 11 || cfg.Description[0] != 1 || cfg.Codec[:5] != "avc1." {
		t.Fatalf("bad config: seq %d codec %q desc %d bytes", seq, cfg.Codec, len(cfg.Description))
	}
	secs := float64(n) / fps
	t.Logf("%d frames, %d keyframes, %s %dx%d, avg %.1f KB/frame, %.0f KB/s",
		len(chunks), keys, cfg.Codec, cfg.Width, cfg.Height, float64(total)/float64(len(chunks))/1024, float64(total)/secs/1024)
}
