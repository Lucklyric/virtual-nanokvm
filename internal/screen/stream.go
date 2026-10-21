package screen

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreGraphics -framework ImageIO -framework Foundation
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <ImageIO/ImageIO.h>
#include <stdlib.h>
#include <string.h>

// The content filter is costly to build, so it is cached until a capture fails.
static SCContentFilter *vnkvm_filter = nil;
static CGFloat vnkvm_aspect = 0;

static int vnkvm_prepare(void) {
	__block SCContentFilter *filter = nil;
	__block CGFloat aspect = 0;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent *content, NSError *error) {
		for (SCDisplay *d in content.displays) {
			if (d.displayID == CGMainDisplayID()) {
				filter = [[SCContentFilter alloc] initWithDisplay:d excludingWindows:@[]];
				aspect = (CGFloat)d.height / (CGFloat)d.width;
			}
		}
		dispatch_semaphore_signal(sem);
	}];
	if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC)) != 0 || filter == nil) {
		return -1;
	}
	vnkvm_filter = filter;
	vnkvm_aspect = aspect;
	return 0;
}

// vnkvm_capture_jpeg captures the main display, cursor included, as a JPEG
// width pixels wide. The caller frees *out.
static int vnkvm_capture_jpeg(int width, double quality, void **out, long *outLen) {
	if (vnkvm_filter == nil && vnkvm_prepare() != 0) {
		return -1;
	}
	SCStreamConfiguration *cfg = [[SCStreamConfiguration alloc] init];
	cfg.width = width;
	cfg.height = (NSInteger)(width * vnkvm_aspect);
	cfg.showsCursor = YES;

	__block CGImageRef image = NULL;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[SCScreenshotManager captureImageWithFilter:vnkvm_filter configuration:cfg
		completionHandler:^(CGImageRef img, NSError *error) {
			if (img != NULL) image = CGImageRetain(img);
			dispatch_semaphore_signal(sem);
		}];
	if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC)) != 0 || image == NULL) {
		vnkvm_filter = nil; // displays may have changed; rebuild next time
		if (image != NULL) CGImageRelease(image);
		return -2;
	}

	CFMutableDataRef data = CFDataCreateMutable(NULL, 0);
	CGImageDestinationRef dest = CGImageDestinationCreateWithData(data, CFSTR("public.jpeg"), 1, NULL);
	NSDictionary *props = @{(__bridge NSString *)kCGImageDestinationLossyCompressionQuality: @(quality)};
	CGImageDestinationAddImage(dest, image, (__bridge CFDictionaryRef)props);
	BOOL ok = CGImageDestinationFinalize(dest);
	CFRelease(dest);
	CGImageRelease(image);
	if (!ok) {
		CFRelease(data);
		return -3;
	}
	*outLen = CFDataGetLength(data);
	*out = malloc(*outLen);
	memcpy(*out, CFDataGetBytePtr(data), *outLen);
	CFRelease(data);
	return 0;
}
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

var captureMu sync.Mutex

// Frame captures the main display, cursor included, as a JPEG width pixels
// wide, in-process with ScreenCaptureKit (fast enough for a live stream).
func Frame(width int, quality float64) ([]byte, error) {
	captureMu.Lock()
	defer captureMu.Unlock()
	var out unsafe.Pointer
	var n C.long
	if rc := C.vnkvm_capture_jpeg(C.int(width), C.double(quality), &out, &n); rc != 0 {
		return nil, fmt.Errorf("screen capture failed (code %d): is Screen Recording allowed for this app?", int(rc))
	}
	defer C.free(out)
	return C.GoBytes(out, C.int(n)), nil
}
