package screen

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreGraphics -framework Foundation
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <stdlib.h>

// vnkvm_capture_rgba captures one window (0: the main display) at full
// resolution, without the cursor, into a malloc'd premultiplied RGBA buffer.
// The pixels keep the frame's own color space, as screencapture does.
// Returns 0, or a negative step number on failure. The caller frees *out.
static int vnkvm_capture_rgba(uint32_t window, uint8_t **out, int *outW, int *outH) {
	CGMainDisplayID(); // opens the window server connection ScreenCaptureKit needs
	__block SCContentFilter *filter = nil;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[SCShareableContent getShareableContentExcludingDesktopWindows:NO onScreenWindowsOnly:NO
		completionHandler:^(SCShareableContent *content, NSError *error) {
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
	if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC)) != 0 || filter == nil) {
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
			dispatch_semaphore_signal(sem);
		}];
	if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC)) != 0 || image == NULL) {
		if (image != NULL) CGImageRelease(image);
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
	if rc := C.vnkvm_capture_rgba(C.uint32_t(window), &buf, &w, &h); rc != 0 {
		return nil, fmt.Errorf("ScreenCaptureKit capture failed (step %d)", -int(rc))
	}
	defer C.free(unsafe.Pointer(buf))
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	copy(img.Pix, unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(w)*int(h)*4))
	return img, nil
}
