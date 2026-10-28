//go:build darwin

package eyes

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreMedia -framework Foundation -framework CoreGraphics
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <CoreMedia/CoreMedia.h>
#include <stdio.h>
#include <string.h>

@interface TapOut : NSObject <SCStreamOutput>
@property (strong) NSMutableData *pcm;   // interleaved float32 stereo
@property double rate;
@end

@implementation TapOut
- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sb ofType:(SCStreamOutputType)type {
	if (type != SCStreamOutputTypeAudio || !CMSampleBufferIsValid(sb)) return;
	const AudioStreamBasicDescription *f = CMAudioFormatDescriptionGetStreamBasicDescription(CMSampleBufferGetFormatDescription(sb));
	if (!f) return;
	self.rate = f->mSampleRate;
	size_t need = 0;
	CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(sb, &need, NULL, 0, NULL, NULL, 0, NULL);
	AudioBufferList *abl = malloc(need);
	CMBlockBufferRef block = NULL;
	if (CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(sb, NULL, abl, need, NULL, NULL,
			kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment, &block) == noErr) {
		UInt32 n = abl->mBuffers[0].mDataByteSize / sizeof(float);
		BOOL planar = (f->mFormatFlags & kAudioFormatFlagIsNonInterleaved) != 0;
		float *l = abl->mBuffers[0].mData;
		if (planar) {
			float *r = abl->mNumberBuffers > 1 ? abl->mBuffers[1].mData : l;
			for (UInt32 i = 0; i < n; i++) { float s[2] = {l[i], r[i]}; [self.pcm appendBytes:s length:sizeof s]; }
		} else {
			[self.pcm appendBytes:l length:abl->mBuffers[0].mDataByteSize];
		}
		CFRelease(block);
	}
	free(abl);
}
@end

static void wle(FILE *f, unsigned v, int n) { for (int i = 0; i < n; i++) fputc((v >> (8*i)) & 255, f); }

// record_app captures seconds of one app's audio into a float WAV.
static int record_app(const char *bundle, double seconds, const char *path, char *err, int errlen) {
	__block SCShareableContent *content = nil;
	dispatch_semaphore_t got = dispatch_semaphore_create(0);
	[SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent *c, NSError *e) { content = c; dispatch_semaphore_signal(got); }];
	dispatch_semaphore_wait(got, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
	if (!content || content.displays.count == 0) { snprintf(err, errlen, "no screen content (Screen Recording permission?)"); return 0; }
	SCRunningApplication *app = nil;
	NSString *want = [NSString stringWithUTF8String:bundle];
	for (SCRunningApplication *a in content.applications) if ([a.bundleIdentifier isEqualToString:want]) app = a;
	if (!app) { snprintf(err, errlen, "%s is not running", bundle); return 0; }
	SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:content.displays[0] includingApplications:@[app] exceptingWindows:@[]];
	SCStreamConfiguration *cfg = [SCStreamConfiguration new];
	cfg.capturesAudio = YES; cfg.sampleRate = 48000; cfg.channelCount = 2;
	cfg.excludesCurrentProcessAudio = YES; cfg.width = 2; cfg.height = 2;
	cfg.minimumFrameInterval = CMTimeMake(1, 1);
	TapOut *out = [TapOut new]; out.pcm = [NSMutableData data];
	SCStream *stream = [[SCStream alloc] initWithFilter:filter configuration:cfg delegate:nil];
	NSError *e = nil;
	dispatch_queue_t q = dispatch_queue_create("listen", DISPATCH_QUEUE_SERIAL);
	if (![stream addStreamOutput:out type:SCStreamOutputTypeAudio sampleHandlerQueue:q error:&e]) { snprintf(err, errlen, "%s", e.localizedDescription.UTF8String); return 0; }
	__block NSError *se = nil;
	dispatch_semaphore_t started = dispatch_semaphore_create(0);
	[stream startCaptureWithCompletionHandler:^(NSError *x) { se = x; dispatch_semaphore_signal(started); }];
	dispatch_semaphore_wait(started, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
	if (se) { snprintf(err, errlen, "%s", se.localizedDescription.UTF8String); return 0; }
	[NSThread sleepForTimeInterval:seconds];
	dispatch_semaphore_t stopped = dispatch_semaphore_create(0);
	[stream stopCaptureWithCompletionHandler:^(NSError *x) { dispatch_semaphore_signal(stopped); }];
	dispatch_semaphore_wait(stopped, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC));
	dispatch_sync(q, ^{});
	FILE *f = fopen(path, "wb");
	if (!f) { snprintf(err, errlen, "cannot write %s", path); return 0; }
	unsigned bytes = (unsigned)out.pcm.length, rate = out.rate > 0 ? (unsigned)out.rate : 48000;
	fwrite("RIFF", 1, 4, f); wle(f, 36 + bytes, 4); fwrite("WAVEfmt ", 1, 8, f);
	wle(f, 16, 4); wle(f, 3, 2); wle(f, 2, 2); wle(f, rate, 4); wle(f, rate * 8, 4); wle(f, 8, 2); wle(f, 32, 2);
	fwrite("data", 1, 4, f); wle(f, bytes, 4); fwrite(out.pcm.bytes, 1, bytes, f); fclose(f);
	if (bytes == 0) { snprintf(err, errlen, "captured no audio: is the app playing, and is its output device working?"); return 0; }
	return 1;
}
*/
import "C"

import (
	"context"
	"errors"
	"unsafe"
)

// Record captures seconds of one app's own audio output to a WAV.
func (Local) Record(ctx context.Context, bundle string, seconds float64, path string) error {
	cb, cp := C.CString(bundle), C.CString(path)
	defer C.free(unsafe.Pointer(cb))
	defer C.free(unsafe.Pointer(cp))
	buf := make([]byte, 256)
	if C.record_app(cb, C.double(seconds), cp, (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))) == 0 {
		return errors.New(C.GoString((*C.char)(unsafe.Pointer(&buf[0]))))
	}
	return nil
}
