// frontmostBundleID — CGWindowList implementation.
//
// NSWorkspace.sharedWorkspace.frontmostApplication requires an AppKit run loop
// and reliably returns nil from a plain CLI binary (no NSApplication).
// CGWindowListCopyWindowInfo is a pure CoreGraphics call that works from any
// process with Screen Recording permission and returns the on-screen window
// stack in front-to-back order.  The first entry (index 0) is always the
// topmost window and its kCGWindowOwnerPID can be resolved to a bundle id via
// NSRunningApplication.
//
// Fallback: if CGWindowListCopyWindowInfo returns an empty list (e.g. no
// on-screen windows at all), we try the NSWorkspace path as a last resort.

#import <AppKit/AppKit.h>
#include <CoreGraphics/CGWindow.h>
#include <stdlib.h>

// _bundleIDForPID resolves a running application's bundle id from its PID.
// Returns NULL (not freed) when the PID is not an NSRunningApplication.
static NSString* _bundleIDForPID(pid_t pid) {
    NSRunningApplication *app =
        [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
    return app ? [app bundleIdentifier] : nil;
}

// frontmostBundleID returns the bundle identifier of the frontmost application,
// or NULL if unavailable.  The caller must free the returned C string.
char* frontmostBundleID(void) {
    // Called many times a second from Go threads that have no autorelease pool
    // of their own: without this every NSRunningApplication returned below
    // (autoreleased) is leaked for the life of the thread.
    @autoreleasepool {
        // --- Primary: CGWindowList (works without a run loop) ---
        CFArrayRef wins = CGWindowListCopyWindowInfo(
            kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements,
            kCGNullWindowID);
        if (wins != NULL) {
            CFIndex count = CFArrayGetCount(wins);
            for (CFIndex i = 0; i < count; i++) {
                NSDictionary *info = (__bridge NSDictionary*)CFArrayGetValueAtIndex(wins, i);
                // Skip windows with layer != 0 (menu bar, overlay, etc.)
                NSNumber *layer = info[(NSString*)kCGWindowLayer];
                if (layer && [layer intValue] != 0) continue;
                NSNumber *pidNum = info[(NSString*)kCGWindowOwnerPID];
                if (!pidNum) continue;
                NSString *bid = _bundleIDForPID((pid_t)[pidNum intValue]);
                if (bid) {
                    char *result = strdup([bid UTF8String]);
                    CFRelease(wins);
                    return result;
                }
            }
            CFRelease(wins);
        }

        // --- Fallback: NSWorkspace (may return nil without a run loop) ---
        NSRunningApplication *app = [[NSWorkspace sharedWorkspace] frontmostApplication];
        if (app == nil) {
            return NULL;
        }
        NSString *bid = [app bundleIdentifier];
        if (bid == nil) {
            return NULL;
        }
        return strdup([bid UTF8String]);
    }
}
