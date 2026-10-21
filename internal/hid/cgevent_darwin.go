package hid

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>

static CGEventSourceRef vnkvm_source(void) {
	static CGEventSourceRef src = NULL;
	if (src == NULL) {
		src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
	}
	return src;
}

static CGPoint vnkvm_cursor(void) {
	CGEventRef e = CGEventCreate(NULL);
	CGPoint p = CGEventGetLocation(e);
	CFRelease(e);
	return p;
}

// Keep the point on some display; the union of display rectangles is not
// always a rectangle, so clamp to the display closest to the point.
static CGPoint vnkvm_clamp(CGPoint p) {
	CGDirectDisplayID ids[16];
	uint32_t n = 0;
	if (CGGetActiveDisplayList(16, ids, &n) != kCGErrorSuccess || n == 0) {
		return p;
	}
	CGPoint best = p;
	double bestDist = -1;
	for (uint32_t i = 0; i < n; i++) {
		CGRect r = CGDisplayBounds(ids[i]);
		CGPoint q = p;
		if (q.x < r.origin.x) q.x = r.origin.x;
		if (q.y < r.origin.y) q.y = r.origin.y;
		if (q.x > r.origin.x + r.size.width - 1) q.x = r.origin.x + r.size.width - 1;
		if (q.y > r.origin.y + r.size.height - 1) q.y = r.origin.y + r.size.height - 1;
		double d = (q.x - p.x) * (q.x - p.x) + (q.y - p.y) * (q.y - p.y);
		if (bestDist < 0 || d < bestDist) {
			best = q;
			bestDist = d;
		}
	}
	return best;
}

static int vnkvm_mouse(double x, double y, int dx, int dy, CGEventType type, int button, int clicks) {
	CGEventRef e = CGEventCreateMouseEvent(vnkvm_source(), type, CGPointMake(x, y), (CGMouseButton)button);
	if (e == NULL) return -1;
	CGEventSetIntegerValueField(e, kCGMouseEventDeltaX, dx);
	CGEventSetIntegerValueField(e, kCGMouseEventDeltaY, dy);
	if (clicks > 0) {
		CGEventSetIntegerValueField(e, kCGMouseEventClickState, clicks);
	}
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
	return 0;
}

static int vnkvm_scroll(int vertical, int horizontal) {
	CGEventRef e = CGEventCreateScrollWheelEvent(vnkvm_source(), kCGScrollEventUnitLine, 2, vertical, horizontal);
	if (e == NULL) return -1;
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
	return 0;
}

static int vnkvm_key(CGKeyCode code, bool down, CGEventFlags flags, bool modifierKey) {
	CGEventRef e = CGEventCreateKeyboardEvent(vnkvm_source(), code, down);
	if (e == NULL) return -1;
	if (modifierKey) {
		CGEventSetType(e, kCGEventFlagsChanged);
	}
	CGEventSetFlags(e, flags);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
	return 0;
}
*/
import "C"

import "errors"

// NewCGEvent returns the synthetic-event backend. It fails when the process
// lacks the Accessibility permission, since macOS would silently drop events.
func NewCGEvent() (*CGEvent, error) {
	if C.AXIsProcessTrusted() == 0 {
		return nil, errors.New("Accessibility permission missing: allow the app that runs vnkvm in System Settings > Privacy & Security > Accessibility")
	}
	return newCGEvent(quartz{}), nil
}

type quartz struct{}

func (quartz) cursor() (float64, float64) {
	p := C.vnkvm_cursor()
	return float64(p.x), float64(p.y)
}

func (quartz) clamp(x, y float64) (float64, float64) {
	p := C.vnkvm_clamp(C.CGPointMake(C.CGFloat(x), C.CGFloat(y)))
	return float64(p.x), float64(p.y)
}

var errPost = errors.New("could not create a Quartz event")

func (quartz) mouse(x, y float64, dx, dy int, button int, down *bool, held int, clicks int) error {
	var t C.CGEventType
	switch {
	case down == nil && held < 0:
		t, button = C.kCGEventMouseMoved, 0
	case down == nil:
		t, button = [...]C.CGEventType{C.kCGEventLeftMouseDragged, C.kCGEventRightMouseDragged, C.kCGEventOtherMouseDragged}[held], held
	case *down:
		t = [...]C.CGEventType{C.kCGEventLeftMouseDown, C.kCGEventRightMouseDown, C.kCGEventOtherMouseDown}[button]
	default:
		t = [...]C.CGEventType{C.kCGEventLeftMouseUp, C.kCGEventRightMouseUp, C.kCGEventOtherMouseUp}[button]
	}
	if C.vnkvm_mouse(C.double(x), C.double(y), C.int(dx), C.int(dy), t, C.int(button), C.int(clicks)) != 0 {
		return errPost
	}
	return nil
}

func (quartz) scroll(vertical, horizontal int) error {
	if C.vnkvm_scroll(C.int(vertical), C.int(horizontal)) != 0 {
		return errPost
	}
	return nil
}

func flags(modifiers uint8) C.CGEventFlags {
	var f C.CGEventFlags
	if modifiers&LeftControl != 0 {
		f |= C.kCGEventFlagMaskControl
	}
	if modifiers&LeftShift != 0 {
		f |= C.kCGEventFlagMaskShift
	}
	if modifiers&LeftOption != 0 {
		f |= C.kCGEventFlagMaskAlternate
	}
	if modifiers&LeftCommand != 0 {
		f |= C.kCGEventFlagMaskCommand
	}
	return f
}

func (quartz) key(code uint16, down bool, modifiers uint8) error {
	if C.vnkvm_key(C.CGKeyCode(code), C.bool(down), flags(modifiers), false) != 0 {
		return errPost
	}
	return nil
}

func (quartz) modifier(code uint16, down bool, modifiers uint8) error {
	if C.vnkvm_key(C.CGKeyCode(code), C.bool(down), flags(modifiers), true) != 0 {
		return errPost
	}
	return nil
}
