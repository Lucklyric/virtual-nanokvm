package screen

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreGraphics -framework ImageIO -framework Foundation -framework VideoToolbox -framework CoreMedia -framework CoreVideo
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <ImageIO/ImageIO.h>
#import <VideoToolbox/VideoToolbox.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

// The content filter is costly to build, so it is cached until a capture fails.
static SCContentFilter *vnkvm_filter = nil;
static CGFloat vnkvm_aspect = 0;

static int vnkvm_prepare(void);

// vnkvm_display_ready rebuilds the filter: 0 when a display can be captured.
static int vnkvm_display_ready(void) {
	vnkvm_filter = nil;
	return vnkvm_prepare();
}

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
// ---- H.264: capture a frame, encode it with VideoToolbox, queue the output.
// Go pulls encoded frames with vnkvm_h264_next, so no Go function is exported.

typedef struct vnkvm_chunk {
	uint8_t *data;
	long len;
	int key;
	int64_t pts;
	struct vnkvm_chunk *next;
} vnkvm_chunk;

static pthread_mutex_t vq_mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t vq_cond = PTHREAD_COND_INITIALIZER;
static vnkvm_chunk *vq_head = NULL, *vq_tail = NULL;
static int vq_len = 0;

static VTCompressionSessionRef vt_session = NULL;
static int vt_width = 0, vt_height = 0, vt_fps = 10, vt_kbps = 3000;

// avcC decoder configuration record and codec string, rebuilt when the
// parameter sets change; vt_desc_seq tells Go to resend the config.
static uint8_t vt_desc[512];
static long vt_desc_len = 0;
static char vt_codec[32];
static int vt_desc_seq = 0;

static void vnkvm_push(vnkvm_chunk *c) {
	pthread_mutex_lock(&vq_mu);
	// A stalled reader must not grow memory: drop the oldest past 60 frames.
	while (vq_len >= 60 && vq_head != NULL) {
		vnkvm_chunk *old = vq_head;
		vq_head = old->next;
		if (vq_head == NULL) vq_tail = NULL;
		free(old->data);
		free(old);
		vq_len--;
	}
	if (vq_tail) vq_tail->next = c; else vq_head = c;
	vq_tail = c;
	vq_len++;
	pthread_cond_signal(&vq_cond);
	pthread_mutex_unlock(&vq_mu);
}

static void vnkvm_update_desc(CMFormatDescriptionRef fmt) {
	const uint8_t *sps = NULL, *pps = NULL;
	size_t spsLen = 0, ppsLen = 0;
	if (CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, 0, &sps, &spsLen, NULL, NULL) != noErr) return;
	if (CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, 1, &pps, &ppsLen, NULL, NULL) != noErr) return;
	if (spsLen < 4 || 11 + spsLen + ppsLen > sizeof(vt_desc)) return;
	uint8_t d[512];
	long n = 0;
	d[n++] = 1; d[n++] = sps[1]; d[n++] = sps[2]; d[n++] = sps[3];
	d[n++] = 0xFF; // 4-byte NAL length prefixes
	d[n++] = 0xE1; // one SPS
	d[n++] = (spsLen >> 8) & 0xFF; d[n++] = spsLen & 0xFF;
	memcpy(d + n, sps, spsLen); n += spsLen;
	d[n++] = 1; // one PPS
	d[n++] = (ppsLen >> 8) & 0xFF; d[n++] = ppsLen & 0xFF;
	memcpy(d + n, pps, ppsLen); n += ppsLen;
	pthread_mutex_lock(&vq_mu);
	if (n != vt_desc_len || memcmp(d, vt_desc, n) != 0) {
		memcpy(vt_desc, d, n);
		vt_desc_len = n;
		snprintf(vt_codec, sizeof(vt_codec), "avc1.%02x%02x%02x", sps[1], sps[2], sps[3]);
		vt_desc_seq++;
	}
	pthread_mutex_unlock(&vq_mu);
}

static void vnkvm_vt_output(void *refcon, void *src, OSStatus status, VTEncodeInfoFlags flags, CMSampleBufferRef sb) {
	if (status != noErr || sb == NULL || !CMSampleBufferDataIsReady(sb)) return;
	int key = 1;
	CFArrayRef att = CMSampleBufferGetSampleAttachmentsArray(sb, false);
	if (att != NULL && CFArrayGetCount(att) > 0) {
		CFDictionaryRef d = CFArrayGetValueAtIndex(att, 0);
		CFBooleanRef notSync = CFDictionaryGetValue(d, kCMSampleAttachmentKey_NotSync);
		key = !(notSync != NULL && CFBooleanGetValue(notSync));
	}
	if (key) vnkvm_update_desc(CMSampleBufferGetFormatDescription(sb));
	CMBlockBufferRef bb = CMSampleBufferGetDataBuffer(sb);
	size_t len = CMBlockBufferGetDataLength(bb);
	vnkvm_chunk *c = calloc(1, sizeof(vnkvm_chunk));
	c->data = malloc(len);
	if (CMBlockBufferCopyDataBytes(bb, 0, len, c->data) != kCMBlockBufferNoErr) {
		free(c->data);
		free(c);
		return;
	}
	c->len = (long)len;
	c->key = key;
	c->pts = (int64_t)(CMTimeGetSeconds(CMSampleBufferGetPresentationTimeStamp(sb)) * 1e6);
	vnkvm_push(c);
}

static void vnkvm_set_int(VTCompressionSessionRef s, CFStringRef key, int v) {
	CFNumberRef n = CFNumberCreate(NULL, kCFNumberIntType, &v);
	VTSessionSetProperty(s, key, n);
	CFRelease(n);
}

static int vnkvm_vt_create(int width, int height) {
	// Low-latency rate control first; fall back to a plain session.
	CFMutableDictionaryRef spec = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFDictionarySetValue(spec, kVTVideoEncoderSpecification_EnableLowLatencyRateControl, kCFBooleanTrue);
	OSStatus st = VTCompressionSessionCreate(NULL, width, height, kCMVideoCodecType_H264, spec, NULL, NULL, vnkvm_vt_output, NULL, &vt_session);
	CFRelease(spec);
	if (st != noErr) {
		st = VTCompressionSessionCreate(NULL, width, height, kCMVideoCodecType_H264, NULL, NULL, NULL, vnkvm_vt_output, NULL, &vt_session);
	}
	if (st != noErr) return -1;
	VTSessionSetProperty(vt_session, kVTCompressionPropertyKey_RealTime, kCFBooleanTrue);
	// No B-frames: decode order must equal display order for the browser.
	VTSessionSetProperty(vt_session, kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse);
	VTSessionSetProperty(vt_session, kVTCompressionPropertyKey_ProfileLevel, kVTProfileLevel_H264_Main_AutoLevel);
	vnkvm_set_int(vt_session, kVTCompressionPropertyKey_MaxKeyFrameInterval, vt_fps * 4);
	vnkvm_set_int(vt_session, kVTCompressionPropertyKey_ExpectedFrameRate, vt_fps);
	vnkvm_set_int(vt_session, kVTCompressionPropertyKey_AverageBitRate, vt_kbps * 1000);
	VTCompressionSessionPrepareToEncodeFrames(vt_session);
	vt_width = width;
	vt_height = height;
	return 0;
}

static void vnkvm_h264_stop(void) {
	if (vt_session != NULL) {
		VTCompressionSessionCompleteFrames(vt_session, kCMTimeInvalid);
		VTCompressionSessionInvalidate(vt_session);
		CFRelease(vt_session);
		vt_session = NULL;
	}
	pthread_mutex_lock(&vq_mu);
	while (vq_head != NULL) {
		vnkvm_chunk *c = vq_head;
		vq_head = c->next;
		free(c->data);
		free(c);
	}
	vq_tail = NULL;
	vq_len = 0;
	vt_desc_len = 0;
	pthread_mutex_unlock(&vq_mu);
}

// vnkvm_h264_encode captures one frame of the main display and submits it.
static int vnkvm_h264_encode(int width, int fps, int kbps, int64_t pts, int forceKey) {
	if (vnkvm_filter == nil && vnkvm_prepare() != 0) return -1;
	int height = ((int)(width * vnkvm_aspect)) & ~1;
	width &= ~1;
	if (vt_session != NULL && (vt_width != width || vt_height != height || vt_fps != fps || vt_kbps != kbps)) {
		vnkvm_h264_stop();
	}
	vt_fps = fps;
	vt_kbps = kbps;
	if (vt_session == NULL && vnkvm_vt_create(width, height) != 0) return -2;

	SCStreamConfiguration *cfg = [[SCStreamConfiguration alloc] init];
	cfg.width = width;
	cfg.height = height;
	cfg.showsCursor = YES;
	cfg.pixelFormat = kCVPixelFormatType_32BGRA;
	__block CMSampleBufferRef frame = NULL;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[SCScreenshotManager captureSampleBufferWithFilter:vnkvm_filter configuration:cfg
		completionHandler:^(CMSampleBufferRef sb, NSError *error) {
			if (sb != NULL) frame = (CMSampleBufferRef)CFRetain(sb);
			dispatch_semaphore_signal(sem);
		}];
	if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC)) != 0 || frame == NULL) {
		vnkvm_filter = nil;
		if (frame != NULL) CFRelease(frame);
		return -3;
	}
	CVImageBufferRef pb = CMSampleBufferGetImageBuffer(frame);
	CFDictionaryRef props = NULL;
	if (forceKey) {
		const void *k[] = {kVTEncodeFrameOptionKey_ForceKeyFrame};
		const void *v[] = {kCFBooleanTrue};
		props = CFDictionaryCreate(NULL, k, v, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	}
	OSStatus st = pb == NULL ? -1 : VTCompressionSessionEncodeFrame(vt_session, pb, CMTimeMake(pts, 1000000), kCMTimeInvalid, props, NULL, NULL);
	if (props != NULL) CFRelease(props);
	CFRelease(frame);
	return st == noErr ? 0 : -4;
}

// vnkvm_h264_next waits up to timeoutMS for an encoded frame. The caller
// frees *data. Returns 1 with a frame, 0 on timeout.
static int vnkvm_h264_next(int timeoutMS, uint8_t **data, long *len, int *key, int64_t *pts) {
	pthread_mutex_lock(&vq_mu);
	if (vq_head == NULL && timeoutMS > 0) {
		struct timespec ts;
		clock_gettime(CLOCK_REALTIME, &ts);
		ts.tv_sec += timeoutMS / 1000;
		ts.tv_nsec += (long)(timeoutMS % 1000) * 1000000;
		if (ts.tv_nsec >= 1000000000) { ts.tv_sec++; ts.tv_nsec -= 1000000000; }
		pthread_cond_timedwait(&vq_cond, &vq_mu, &ts);
	}
	vnkvm_chunk *c = vq_head;
	if (c != NULL) {
		vq_head = c->next;
		if (vq_head == NULL) vq_tail = NULL;
		vq_len--;
	}
	pthread_mutex_unlock(&vq_mu);
	if (c == NULL) return 0;
	*data = c->data;
	*len = c->len;
	*key = c->key;
	*pts = c->pts;
	free(c);
	return 1;
}

// vnkvm_h264_config copies the current avcC record and codec string.
static int vnkvm_h264_config(uint8_t *desc, long *descLen, char *codec, int *w, int *h) {
	pthread_mutex_lock(&vq_mu);
	int seq = vt_desc_seq;
	memcpy(desc, vt_desc, vt_desc_len);
	*descLen = vt_desc_len;
	memcpy(codec, vt_codec, sizeof(vt_codec));
	*w = vt_width;
	*h = vt_height;
	pthread_mutex_unlock(&vq_mu);
	return seq;
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
		return nil, captureErr("screen capture", int(rc))
	}
	defer C.free(out)
	return C.GoBytes(out, C.int(n)), nil
}

// H264Chunk is one encoded frame in AVCC form (4-byte length-prefixed NALs).
type H264Chunk struct {
	Data []byte
	Key  bool
	PTS  int64 // microseconds
}

// H264Config describes the stream for a decoder: the avcC record and codec string.
type H264Config struct {
	Codec       string `json:"codec"`
	Description []byte `json:"description"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// H264Encode captures the main display and submits it to the H.264 encoder,
// which runs in real time at fps and about kbps. Output arrives via H264Next.
func H264Encode(width, fps, kbps int, pts int64, forceKey bool) error {
	captureMu.Lock()
	defer captureMu.Unlock()
	key := 0
	if forceKey {
		key = 1
	}
	if rc := C.vnkvm_h264_encode(C.int(width), C.int(fps), C.int(kbps), C.int64_t(pts), C.int(key)); rc != 0 {
		return captureErr("h264 encode", int(rc))
	}
	return nil
}

// H264Next returns the next encoded frame, waiting up to timeoutMS.
func H264Next(timeoutMS int) (H264Chunk, bool) {
	var data *C.uint8_t
	var n C.long
	var key C.int
	var pts C.int64_t
	if C.vnkvm_h264_next(C.int(timeoutMS), &data, &n, &key, &pts) == 0 {
		return H264Chunk{}, false
	}
	defer C.free(unsafe.Pointer(data))
	return H264Chunk{Data: C.GoBytes(unsafe.Pointer(data), C.int(n)), Key: key != 0, PTS: int64(pts)}, true
}

// H264CurrentConfig returns the decoder config and a sequence number that
// changes whenever the config does (0 before the first keyframe).
func H264CurrentConfig() (H264Config, int) {
	var desc [512]C.uint8_t
	var n C.long
	var codec [32]C.char
	var w, h C.int
	seq := int(C.vnkvm_h264_config(&desc[0], &n, &codec[0], &w, &h))
	return H264Config{
		Codec:       C.GoString(&codec[0]),
		Description: C.GoBytes(unsafe.Pointer(&desc[0]), C.int(n)),
		Width:       int(w),
		Height:      int(h),
	}, seq
}

// H264Stop ends the encoder session and drops queued frames.
func H264Stop() {
	captureMu.Lock()
	defer captureMu.Unlock()
	C.vnkvm_h264_stop()
}

// captureErr explains a capture failure. Code -1 means no display was
// offered: the Mac is locked, the display sleeps, or Screen Recording is off.
func captureErr(what string, rc int) error {
	if rc == -1 {
		return fmt.Errorf("%s: no display to capture (Mac locked or display asleep, or Screen Recording not allowed for this app)", what)
	}
	return fmt.Errorf("%s failed (code %d)", what, rc)
}

// ScreenAccess reports whether this process may record the screen. With
// request, macOS shows its permission dialog (once per app).
func ScreenAccess(request bool) bool {
	if request {
		return bool(C.CGRequestScreenCaptureAccess())
	}
	return bool(C.CGPreflightScreenCaptureAccess())
}

// DisplayReady reports whether a display can be captured now; it cannot while
// the Mac is locked or the display sleeps.
func DisplayReady() bool {
	captureMu.Lock()
	defer captureMu.Unlock()
	return C.vnkvm_display_ready() == 0
}
