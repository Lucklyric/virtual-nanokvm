// Package ax acts on app controls through the macOS Accessibility API, the
// way VoiceOver does: the control at a screen point is asked to press itself.
// No cursor moves and the app stays in the background. It needs the
// Accessibility permission, as the sys input does.
package ax

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <string.h>
#include <stdlib.h>
#include <unistd.h>

static void vnkvm_copy(AXUIElementRef el, CFStringRef attr, char *out, int n) {
	out[0] = 0;
	CFTypeRef v = NULL;
	if (AXUIElementCopyAttributeValue(el, attr, &v) == kAXErrorSuccess && v) {
		if (CFGetTypeID(v) == CFStringGetTypeID()) {
			CFStringGetCString(v, out, n, kCFStringEncodingUTF8);
		}
		CFRelease(v);
	}
}

static int vnkvm_has_action(AXUIElementRef el, CFStringRef action) {
	CFArrayRef names = NULL;
	if (AXUIElementCopyActionNames(el, &names) != kAXErrorSuccess || !names) return 0;
	int ok = CFArrayContainsValue(names, CFRangeMake(0, CFArrayGetCount(names)), action);
	CFRelease(names);
	return ok;
}

// Controls a keyboard can trigger once focused (Space or Enter). Chromium
// lists no press action for web content, so these are focused instead.
static int vnkvm_is_keyable(const char *role) {
	return !strcmp(role, "AXButton") || !strcmp(role, "AXLink") || !strcmp(role, "AXCheckBox") ||
		!strcmp(role, "AXRadioButton") || !strcmp(role, "AXPopUpButton") || !strcmp(role, "AXMenuItem");
}

static int vnkvm_is_text(const char *role) {
	return !strcmp(role, "AXTextField") || !strcmp(role, "AXTextArea") ||
		!strcmp(role, "AXComboBox") || !strcmp(role, "AXSearchField");
}

// vnkvm_ax_act finds the control of app pid at global point (x, y) and acts
// on it: press (menu = 0) or show its context menu (menu = 1). Text fields are
// focused instead of pressed. It walks up a few parents, since the deepest
// element is often a label inside the button.
// Returns 0 pressed, 1 focused, 2 menu shown, 3 focused for a key press,
// -1 no element, -2 nothing to act on, -3 the action failed. role and label describe the element used (or
// the deepest one found, on failure).
static int vnkvm_ax_act_once(int pid, double x, double y, int menu, char *role, char *label, int n);

// Chromium and Electron build their page tree for Accessibility only when an
// assistive app asks. AXManualAccessibility is that request; unlike
// AXEnhancedUserInterface it does not change window behavior.
static int vnkvm_ax_act(int pid, double x, double y, int menu, char *role, char *label, int n) {
	int rc = vnkvm_ax_act_once(pid, x, y, menu, role, label, n);
	if (rc != -2 && rc != -1) return rc;
	AXUIElementRef app = AXUIElementCreateApplication(pid);
	// Chromium may answer "unsupported" and still build the tree, so retry anyway.
	AXUIElementSetAttributeValue(app, CFSTR("AXManualAccessibility"), kCFBooleanTrue);
	CFRelease(app);
	for (int i = 0; i < 10 && (rc == -2 || rc == -1); i++) {
		usleep(150000); // the app builds its tree asynchronously
		rc = vnkvm_ax_act_once(pid, x, y, menu, role, label, n);
	}
	return rc;
}

static int vnkvm_ax_act_once(int pid, double x, double y, int menu, char *role, char *label, int n) {
	AXUIElementRef app = AXUIElementCreateApplication(pid);
	AXUIElementSetMessagingTimeout(app, 2.0);
	AXUIElementRef el = NULL;
	role[0] = 0;
	label[0] = 0;
	if (AXUIElementCopyElementAtPosition(app, x, y, &el) != kAXErrorSuccess || !el) {
		CFRelease(app);
		return -1;
	}
	vnkvm_copy(el, kAXRoleAttribute, role, n);
	int rc = -2;
	for (int depth = 0; depth < 5 && el; depth++) {
		char r[64], l[256];
		vnkvm_copy(el, kAXRoleAttribute, r, sizeof r);
		vnkvm_copy(el, kAXDescriptionAttribute, l, sizeof l);
		if (!l[0]) vnkvm_copy(el, kAXTitleAttribute, l, sizeof l);
		CFStringRef action = menu ? kAXShowMenuAction : kAXPressAction;
		if (!menu && vnkvm_is_text(r)) {
			rc = AXUIElementSetAttributeValue(el, kAXFocusedAttribute, kCFBooleanTrue) == kAXErrorSuccess ? 1 : -3;
		} else if (vnkvm_has_action(el, action)) {
			rc = AXUIElementPerformAction(el, action) == kAXErrorSuccess ? (menu ? 2 : 0) : -3;
		} else if (!menu && vnkvm_is_keyable(r)) {
			rc = AXUIElementSetAttributeValue(el, kAXFocusedAttribute, kCFBooleanTrue) == kAXErrorSuccess ? 3 : -3;
		}
		if (rc != -2) {
			strncpy(role, r, n - 1);
			strncpy(label, l, n - 1);
			break;
		}
		AXUIElementRef parent = NULL;
		if (AXUIElementCopyAttributeValue(el, kAXParentAttribute, (CFTypeRef *)&parent) != kAXErrorSuccess) parent = NULL;
		CFRelease(el);
		el = parent;
	}
	if (el) CFRelease(el);
	CFRelease(app);
	return rc;
}
// vnkvm_ax_child finds the child of el whose title is name (case-sensitive).
static AXUIElementRef vnkvm_ax_child(AXUIElementRef el, const char *name) {
	CFArrayRef kids = NULL;
	if (AXUIElementCopyAttributeValue(el, kAXChildrenAttribute, (CFTypeRef *)&kids) != kAXErrorSuccess || !kids) return NULL;
	AXUIElementRef found = NULL;
	for (CFIndex i = 0; i < CFArrayGetCount(kids) && !found; i++) {
		AXUIElementRef k = (AXUIElementRef)CFArrayGetValueAtIndex(kids, i);
		char t[256];
		vnkvm_copy(k, kAXTitleAttribute, t, sizeof t);
		if (!strcmp(t, name)) {
			found = (AXUIElementRef)CFRetain(k);
		} else {
			char r[64];
			vnkvm_copy(k, kAXRoleAttribute, r, sizeof r);
			if (!strcmp(r, "AXMenu")) found = vnkvm_ax_child(k, name); // a menu sits between items
		}
	}
	CFRelease(kids);
	return found;
}

// vnkvm_ax_menu presses the menu item at path (names separated by \x1f) in
// app pid's menu bar. Returns 0 ok, -1 no menu bar, -2 not found (depth in
// *missing), -3 the press failed.
static int vnkvm_ax_menu(int pid, const char *path, int *missing) {
	AXUIElementRef app = AXUIElementCreateApplication(pid);
	AXUIElementSetMessagingTimeout(app, 2.0);
	AXUIElementRef el = NULL;
	if (AXUIElementCopyAttributeValue(app, kAXMenuBarAttribute, (CFTypeRef *)&el) != kAXErrorSuccess || !el) {
		CFRelease(app);
		return -1;
	}
	char buf[1024];
	strncpy(buf, path, sizeof buf - 1);
	buf[sizeof buf - 1] = 0;
	int depth = 0;
	for (char *name = strtok(buf, "\x1f"); name; name = strtok(NULL, "\x1f"), depth++) {
		AXUIElementRef next = vnkvm_ax_child(el, name);
		CFRelease(el);
		el = next;
		if (!el) {
			*missing = depth;
			CFRelease(app);
			return -2;
		}
	}
	int rc = AXUIElementPerformAction(el, kAXPressAction) == kAXErrorSuccess ? 0 : -3;
	CFRelease(el);
	CFRelease(app);
	return rc;
}
*/
import "C"

import (
	"fmt"
	"strings"
	"unsafe"
)

// Result says which control was used and what happened to it.
type Result struct {
	Role  string // e.g. AXButton
	Label string // its description or title, often empty
	Did   string // pressed, focused, menu, or key: focused, and the caller presses Key
	Key   string // for Did == "key": space, or enter for links
}

// Act presses the control of app pid at global point (x, y), focuses it when
// it is a text field, or opens its context menu when menu is set.
func Act(pid int, x, y float64, menu bool) (Result, error) {
	var role, label [256]C.char
	m := 0
	if menu {
		m = 1
	}
	rc := C.vnkvm_ax_act(C.int(pid), C.double(x), C.double(y), C.int(m), &role[0], &label[0], C.int(len(role)))
	r := Result{Role: C.GoString(&role[0]), Label: C.GoString(&label[0])}
	switch rc {
	case 0:
		r.Did = "pressed"
	case 1:
		r.Did = "focused"
	case 3:
		r.Did, r.Key = "key", "space"
		if r.Role == "AXLink" {
			r.Key = "enter"
		}
	case 2:
		r.Did = "menu"
	case -1:
		return r, fmt.Errorf("background: the app reports no control at that point")
	case -2:
		return r, fmt.Errorf("background: nothing to press there (found %s); web content or a canvas may need foreground mode", r.Role)
	default:
		return r, fmt.Errorf("background: %s refused the action", r.Role)
	}
	return r, nil
}

// Menu chooses a menu bar item of app pid, such as "Edit > Select All". It
// works with the app in the background, where cmd shortcuts do not arrive.
func Menu(pid int, path string) error {
	var parts []string
	for _, p := range strings.Split(path, ">") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) < 2 {
		return fmt.Errorf("give a menu path such as Edit > Select All")
	}
	cpath := C.CString(strings.Join(parts, "\x1f"))
	defer C.free(unsafe.Pointer(cpath))
	var missing C.int
	switch C.vnkvm_ax_menu(C.int(pid), cpath, &missing) {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("the app has no menu bar it will share")
	case -2:
		return fmt.Errorf("no menu item %q (names must match the menu exactly)", parts[missing])
	}
	return fmt.Errorf("the app refused the menu item %q", path)
}
