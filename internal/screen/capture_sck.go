package screen

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreGraphics -framework Foundation
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <stdlib.h>

// vnkvm_capture_rgba captures one window (0: the main display) at full
// resolution, without the cursor, into a malloc'd premultiplied RGBA buffer.
// The pixels keep the frame's own color space, as screencapture does.
// Returns 0, or a negative step number on failure, with what macOS reported
// in *why when it said anything. The caller frees *out and *why.
static int vnkvm_capture_rgba(uint32_t window, uint8_t **out, int *outW, int *outH, char **why) {
	CGMainDisplayID(); // opens the window server connection ScreenCaptureKit needs
	__block SCContentFilter *filter = nil;
	__block NSString *reason = nil;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[SCShareableContent getShareableContentExcludingDesktopWindows:NO onScreenWindowsOnly:NO
		completionHandler:^(SCShareableContent *content, NSError *error) {
			if (error != nil) reason = error.localizedDescription;
			if (window == 0) {
				for (SCDisplay *d in content.displays) {
					if (d.displayID == CGMainDisplayID()) {
						filter = [[SCContentFilter alloc] initWithDisplay:d excludingWindows:@[]];
					}
				}
			} else {
				for (SCWindow *w in content.windows) {
					if (w.windowID == window) {
						filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:w];
					}
				}
			}
			dispatch_semaphore_signal(sem);
		}];
	if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC)) != 0) {
		*why = strdup("no answer within 3 s");
		return -1;
	}
	if (filter == nil) {
		if (reason != nil) *why = strdup(reason.UTF8String);
		return -1;
	}
	CGRect rect = filter.contentRect;
	CGFloat scale = filter.pointPixelScale;
	SCStreamConfiguration *cfg = [[SCStreamConfiguration alloc] init];
	cfg.width = (size_t)llround(rect.size.width * scale);
	cfg.height = (size_t)llround(rect.size.height * scale);
	cfg.showsCursor = NO;
	cfg.ignoreShadowsSingleWindow = YES;

	__block CGImageRef image = NULL;
	sem = dispatch_semaphore_create(0);
	[SCScreenshotManager captureImageWithFilter:filter configuration:cfg
		completionHandler:^(CGImageRef img, NSError *error) {
			if (img != NULL) image = CGImageRetain(img);
			if (error != nil) reason = error.localizedDescription;
			dispatch_semaphore_signal(sem);
		}];
	if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC)) != 0) {
		*why = strdup("no answer within 3 s");
		return -2;
	}
	if (image == NULL) {
		if (reason != nil) *why = strdup(reason.UTF8String);
		return -2;
	}

	size_t w = CGImageGetWidth(image), h = CGImageGetHeight(image);
	CGColorSpaceRef space = CGImageGetColorSpace(image);
	CGColorSpaceRef own = NULL;
	if (space == NULL || CGColorSpaceGetModel(space) != kCGColorSpaceModelRGB) {
		own = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
		space = own;
	}
	uint8_t *buf = malloc(w * h * 4);
	CGContextRef ctx = buf == NULL ? NULL : CGBitmapContextCreate(buf, w, h, 8, w * 4, space,
		kCGImageAlphaPremultipliedLast | kCGBitmapByteOrder32Big);
	if (ctx == NULL) {
		free(buf);
		if (own != NULL) CGColorSpaceRelease(own);
		CGImageRelease(image);
		return -3;
	}
	CGContextSetBlendMode(ctx, kCGBlendModeCopy);
	CGContextDrawImage(ctx, CGRectMake(0, 0, w, h), image);
	CGContextRelease(ctx);
	if (own != NULL) CGColorSpaceRelease(own);
	CGImageRelease(image);
	*out = buf;
	*outW = (int)w;
	*outH = (int)h;
	return 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"unsafe"
)

// captureSCK grabs the main display (window 0) or one window in process with
// ScreenCaptureKit. It avoids starting screencapture for each frame, which can
// take seconds while another app streams the screen.
func captureSCK(window uint32) (*image.RGBA, error) {
	var buf *C.uint8_t
	var w, h C.int
	var why *C.char
	if rc := C.vnkvm_capture_rgba(C.uint32_t(window), &buf, &w, &h, &why); rc != 0 {
		// What failed, in order: finding the window or display, taking the
		// picture, reading its pixels.
		step := [...]string{"the window or display was not offered for capture", "no image came back", "the image could not be read"}[-int(rc)-1]
		if why != nil {
			defer C.free(unsafe.Pointer(why))
			return nil, fmt.Errorf("%s: %s", step, C.GoString(why))
		}
		return nil, errors.New(step)
	}
	defer C.free(unsafe.Pointer(buf))
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	copy(img.Pix, unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(w)*int(h)*4))
	return img, nil
}
