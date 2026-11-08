// isAppRunning — process-presence check by bundle identifier.
//
// Deliberately NOT NSWorkspace.frontmostApplication: that one needs an AppKit
// run loop and returns nil from a plain CLI binary (see frontmost_darwin.m's
// header comment, learned the hard way). +[NSRunningApplication
// runningApplicationsWithBundleIdentifier:] is a Launch Services query with no
// such requirement — it answers "does a process with this bundle id exist right
// now", which is exactly the signal the HUD gate wants and is independent of
// which app is focused, whether Resolve has a window open, and whether the
// scripting sidecar happens to be responding.

#import <AppKit/AppKit.h>

// isAppRunning returns 1 when at least one running application carries the
// given bundle identifier, 0 otherwise (including a NULL/invalid argument).
int isAppRunning(const char *bundleID) {
    if (bundleID == NULL) return 0;
    @autoreleasepool {
        NSString *bid = [NSString stringWithUTF8String:bundleID];
        if (bid == nil) return 0;
        NSArray<NSRunningApplication *> *apps =
            [NSRunningApplication runningApplicationsWithBundleIdentifier:bid];
        return (apps != nil && [apps count] > 0) ? 1 : 0;
    }
}
