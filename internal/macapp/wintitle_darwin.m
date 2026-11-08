// resolveWindowTitle — returns the window title of the frontmost DaVinci Resolve
// window using CGWindowListCopyWindowInfo + kCGWindowName.
//
// The result is cached for up to ~1 second inside the calling Go layer
// (windowTitleCacheMs). This function always does a live query; the cache
// lives in the Go wrapper resolveWindowTitle().

#import <AppKit/AppKit.h>
#include <CoreGraphics/CGWindow.h>
#include <stdlib.h>

static const char* resolveBundleIDStr = "com.blackmagic-design.DaVinciResolve";

// resolveWindowTitleC returns the kCGWindowName of the frontmost DaVinci
// Resolve window, or NULL if Resolve has no on-screen window.
// The caller must free() the returned string.
char* resolveWindowTitleC(void) {
    // Go threads have no autorelease pool: contain what the loop autoreleases.
    @autoreleasepool {
        CFArrayRef wins = CGWindowListCopyWindowInfo(
            kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements,
            kCGNullWindowID);
        if (wins == NULL) return NULL;

        char *result = NULL;
        CFIndex count = CFArrayGetCount(wins);
        for (CFIndex i = 0; i < count && result == NULL; i++) {
            NSDictionary *info = (__bridge NSDictionary*)CFArrayGetValueAtIndex(wins, i);
            // Only inspect Resolve windows (match by owner name or PID→bundle).
            NSNumber *pidNum = info[(NSString*)kCGWindowOwnerPID];
            if (!pidNum) continue;
            NSRunningApplication *app =
                [NSRunningApplication runningApplicationWithProcessIdentifier:(pid_t)[pidNum intValue]];
            if (!app) continue;
            NSString *bid = [app bundleIdentifier];
            if (!bid || strcmp([bid UTF8String], resolveBundleIDStr) != 0) continue;
            // Found a Resolve window — grab its title.
            NSString *name = info[(NSString*)kCGWindowName];
            if (name && [name length] > 0) {
                result = strdup([name UTF8String]);
            } else {
                // Window exists but has no title yet; keep scanning.
            }
        }
        CFRelease(wins);
        return result;
    }
}
