// runningApps / appNameForBundleID / selfBundleID: Launch Services style
// queries for the editor's app picker. NSWorkspace.runningApplications needs no
// Screen Recording permission and no on-screen window.

#import <AppKit/AppKit.h>
#include <stdlib.h>
#include <string.h>

// runningApps returns "bundleID\tname\n" for every running application with a
// regular activation policy (Dock apps) and a bundle id. Caller frees.
char* runningApps(void) {
    @autoreleasepool {
        NSMutableString *out = [NSMutableString string];
        for (NSRunningApplication *a in [[NSWorkspace sharedWorkspace] runningApplications]) {
            if (a.activationPolicy != NSApplicationActivationPolicyRegular) continue;
            NSString *bid = a.bundleIdentifier;
            if (bid == nil || [bid length] == 0) continue;
            NSString *name = a.localizedName ? a.localizedName : bid;
            name = [name stringByReplacingOccurrencesOfString:@"\t" withString:@" "];
            name = [name stringByReplacingOccurrencesOfString:@"\n" withString:@" "];
            [out appendFormat:@"%@\t%@\n", bid, name];
        }
        return strdup([out UTF8String]);
    }
}

// appNameForBundleID returns the localized name of a running app, or NULL.
char* appNameForBundleID(const char *bundleID) {
    if (bundleID == NULL) return NULL;
    @autoreleasepool {
        NSString *bid = [NSString stringWithUTF8String:bundleID];
        if (bid == nil) return NULL;
        NSArray<NSRunningApplication *> *apps =
            [NSRunningApplication runningApplicationsWithBundleIdentifier:bid];
        if ([apps count] == 0) return NULL;
        NSString *n = apps[0].localizedName;
        return n ? strdup([n UTF8String]) : NULL;
    }
}

// selfBundleID returns this process's own bundle id (empty outside a .app).
char* selfBundleID(void) {
    @autoreleasepool {
        NSString *b = [[NSBundle mainBundle] bundleIdentifier];
        return strdup(b ? [b UTF8String] : "");
    }
}
