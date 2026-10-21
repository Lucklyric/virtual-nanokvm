package screen

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>

typedef struct {
	uint32_t id;
	int32_t pid;
	CGRect bounds;
	char app[128];
	char title[256];
} vnkvm_window;

static void vnkvm_copy_string(CFDictionaryRef d, CFStringRef key, char *out, size_t n) {
	out[0] = 0;
	CFStringRef s = CFDictionaryGetValue(d, key);
	if (s != NULL && CFGetTypeID(s) == CFStringGetTypeID()) {
		CFStringGetCString(s, out, n, kCFStringEncodingUTF8);
	}
}

// vnkvm_windows fills out with on-screen app windows (layer 0), front to back.
static int vnkvm_windows(vnkvm_window *out, int max) {
	CFArrayRef list = CGWindowListCopyWindowInfo(
		kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	if (list == NULL) return 0;
	int n = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(list) && n < max; i++) {
		CFDictionaryRef d = CFArrayGetValueAtIndex(list, i);
		int layer = -1;
		CFNumberRef num = CFDictionaryGetValue(d, kCGWindowLayer);
		if (num != NULL) CFNumberGetValue(num, kCFNumberIntType, &layer);
		if (layer != 0) continue;
		vnkvm_window *w = &out[n];
		num = CFDictionaryGetValue(d, kCGWindowNumber);
		if (num == NULL || !CFNumberGetValue(num, kCFNumberSInt32Type, &w->id)) continue;
		num = CFDictionaryGetValue(d, kCGWindowOwnerPID);
		w->pid = 0;
		if (num != NULL) CFNumberGetValue(num, kCFNumberSInt32Type, &w->pid);
		CFDictionaryRef b = CFDictionaryGetValue(d, kCGWindowBounds);
		if (b == NULL || !CGRectMakeWithDictionaryRepresentation(b, &w->bounds)) continue;
		vnkvm_copy_string(d, kCGWindowOwnerName, w->app, sizeof(w->app));
		vnkvm_copy_string(d, kCGWindowName, w->title, sizeof(w->title));
		n++;
	}
	CFRelease(list);
	return n;
}
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Window is an on-screen app window. Bounds are global display points.
type Window struct {
	ID     uint32  `json:"id"`
	PID    int     `json:"pid"`
	App    string  `json:"app"`
	Title  string  `json:"title"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Windows lists on-screen app windows, frontmost first. Titles are empty
// without the Screen Recording permission.
func Windows() []Window {
	var buf [256]C.vnkvm_window
	n := int(C.vnkvm_windows(&buf[0], C.int(len(buf))))
	out := make([]Window, 0, n)
	for _, w := range buf[:n] {
		out = append(out, Window{
			ID:     uint32(w.id),
			PID:    int(w.pid),
			App:    C.GoString(&w.app[0]),
			Title:  C.GoString(&w.title[0]),
			X:      float64(w.bounds.origin.x),
			Y:      float64(w.bounds.origin.y),
			Width:  float64(w.bounds.size.width),
			Height: float64(w.bounds.size.height),
		})
	}
	return out
}

// FindWindow returns the window with id, or, when id is 0, the frontmost
// window whose app name contains app (case-insensitive).
func FindWindow(id uint32, app string) (Window, error) {
	for _, w := range Windows() {
		if id != 0 && w.ID == id {
			return w, nil
		}
		if id == 0 && app != "" && strings.Contains(strings.ToLower(w.App), strings.ToLower(app)) {
			return w, nil
		}
	}
	if id != 0 {
		return Window{}, fmt.Errorf("no on-screen window with id %d", id)
	}
	return Window{}, fmt.Errorf("no on-screen window for app %q", app)
}

// WindowAt returns the frontmost window containing the global point (x, y).
func WindowAt(x, y float64) (Window, bool) {
	for _, w := range Windows() {
		if x >= w.X && y >= w.Y && x < w.X+w.Width && y < w.Y+w.Height {
			return w, true
		}
	}
	return Window{}, false
}

// WindowShot captures one window, even when other windows cover it, as a JPEG
// resized to width pixels.
func WindowShot(ctx context.Context, id uint32, width int) ([]byte, error) {
	dir, err := os.MkdirTemp("", "vnkvm-win-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	raw := filepath.Join(dir, "raw.png")
	out := filepath.Join(dir, "shot.jpg")
	if b, err := exec.CommandContext(ctx, "screencapture", "-x", "-o", "-l", fmt.Sprint(id), "-t", "png", raw).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("screencapture: %v: %s", err, b)
	}
	if _, err := os.Stat(raw); err != nil {
		return nil, fmt.Errorf("screencapture wrote no image (Screen Recording permission missing?)")
	}
	if b, err := exec.CommandContext(ctx, "sips",
		"--resampleWidth", fmt.Sprint(width),
		"-s", "format", "jpeg", "-s", "formatOptions", "75",
		raw, "--out", out).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sips: %v: %s", err, b)
	}
	return os.ReadFile(out)
}
