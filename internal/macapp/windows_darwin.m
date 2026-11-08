// windows_darwin.m: read-only window / display / pixel queries for the mouse
// calibration (internal/mousecal). Nothing here posts input.

#import <AppKit/AppKit.h>
#import <CoreGraphics/CoreGraphics.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <stdlib.h>
#include <string.h>

// appWindowsC returns one line per on-screen normal window of the app, front
// to back: "x\ty\tw\th\ttitle\n" (global points, top-left origin). Window
// titles need Screen Recording and are empty without it. NULL when the app is
// not running. Caller frees.
char* appWindowsC(const char *bundleID) {
    @autoreleasepool {
        NSString *bid = [NSString stringWithUTF8String:bundleID];
        NSMutableSet<NSNumber *> *pids = [NSMutableSet set];
        for (NSRunningApplication *a in [[NSWorkspace sharedWorkspace] runningApplications]) {
            if (a.bundleIdentifier && [a.bundleIdentifier caseInsensitiveCompare:bid] == NSOrderedSame) {
                [pids addObject:@(a.processIdentifier)];
            }
        }
        if ([pids count] == 0) return NULL;
        NSMutableString *out = [NSMutableString string];
        CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
        if (list == NULL) return strdup("");
        for (NSDictionary *w in (__bridge NSArray *)list) {
            if (![pids containsObject:w[(id)kCGWindowOwnerPID]]) continue;
            if ([w[(id)kCGWindowLayer] intValue] != 0) continue;
            if ([w[(id)kCGWindowAlpha] doubleValue] <= 0.01) continue;
            CGRect r;
            if (!CGRectMakeWithDictionaryRepresentation((__bridge CFDictionaryRef)w[(id)kCGWindowBounds], &r)) continue;
            NSString *t = w[(id)kCGWindowName] ? w[(id)kCGWindowName] : @"";
            t = [t stringByReplacingOccurrencesOfString:@"\t" withString:@" "];
            t = [t stringByReplacingOccurrencesOfString:@"\n" withString:@" "];
            [out appendFormat:@"%f\t%f\t%f\t%f\t%@\n", r.origin.x, r.origin.y, r.size.width, r.size.height, t];
        }
        CFRelease(list);
        return strdup([out UTF8String]);
    }
}

// displaysC returns one line per active display: "id\tx\ty\tw\th\tscale\tname\n".
char* displaysC(void) {
    @autoreleasepool {
        CGDirectDisplayID ids[16];
        uint32_t n = 0;
        if (CGGetActiveDisplayList(16, ids, &n) != kCGErrorSuccess) return strdup("");
        NSMutableDictionary<NSNumber *, NSString *> *names = [NSMutableDictionary dictionary];
        for (NSScreen *s in [NSScreen screens]) {
            NSNumber *num = s.deviceDescription[@"NSScreenNumber"];
            if (num) names[num] = s.localizedName ? s.localizedName : @"";
        }
        NSMutableString *out = [NSMutableString string];
        for (uint32_t i = 0; i < n; i++) {
            CGRect b = CGDisplayBounds(ids[i]);
            double scale = 1.0;
            CGDisplayModeRef m = CGDisplayCopyDisplayMode(ids[i]);
            if (m) {
                if (CGDisplayModeGetWidth(m) > 0) scale = (double)CGDisplayModeGetPixelWidth(m) / (double)CGDisplayModeGetWidth(m);
                CGDisplayModeRelease(m);
            }
            NSString *nm = names[@(ids[i])] ? names[@(ids[i])] : @"";
            [out appendFormat:@"%u\t%f\t%f\t%f\t%f\t%f\t%@\n", ids[i], b.origin.x, b.origin.y, b.size.width, b.size.height, scale, nm];
        }
        return strdup([out UTF8String]);
    }
}

// grabRectC captures the screen inside r (global points) as tightly packed RGBA
// bytes (w*h*4). Needs Screen Recording and macOS 15.2+ (ScreenCaptureKit
// replaced CGWindowListCreateImage). Returns 0 and sets *err on failure.
int grabRectC(double x, double y, double w, double h, unsigned char **out, int *ow, int *oh, char **err) {
    *out = NULL;
    if (@available(macOS 15.2, *)) {
        __block CGImageRef img = NULL;
        __block NSString *msg = nil;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        [SCScreenshotManager captureImageInRect:CGRectMake(x, y, w, h) completionHandler:^(CGImageRef image, NSError *error) {
            if (image) img = CGImageRetain(image); else msg = error ? [error localizedDescription] : @"no image";
            dispatch_semaphore_signal(sem);
        }];
        if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC)) != 0) {
            *err = strdup("the screen capture timed out");
            return 0;
        }
        if (!img) { *err = strdup([msg UTF8String]); return 0; }
        size_t iw = CGImageGetWidth(img), ih = CGImageGetHeight(img);
        unsigned char *buf = calloc(iw * ih * 4, 1);
        CGColorSpaceRef cs = CGColorSpaceCreateDeviceRGB();
        CGContextRef ctx = CGBitmapContextCreate(buf, iw, ih, 8, iw * 4, cs, kCGImageAlphaPremultipliedLast | kCGBitmapByteOrder32Big);
        CGContextDrawImage(ctx, CGRectMake(0, 0, iw, ih), img);
        CGContextRelease(ctx);
        CGColorSpaceRelease(cs);
        CGImageRelease(img);
        *out = buf; *ow = (int)iw; *oh = (int)ih;
        return 1;
    }
    *err = strdup("reading the screen needs macOS 15.2 or later");
    return 0;
}
