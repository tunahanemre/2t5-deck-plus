// Finder selection + colour-tag read/write.
//
// # Two very different kinds of call live in this file
//
// 1. finderSelection() sends an APPLE EVENT to Finder (there is no public API
//    for reading another app's selection). It therefore needs TCC's
//    Automation grant and a stable thread that can service the reply.
// 2. finderReadTags()/finderWriteTags() are plain NSURL resource-value calls
//    on the local filesystem. No Apple Event, no run loop, no permission
//    beyond ordinary file access -- so they run directly on whatever thread
//    cgo hands them and must NOT be forced onto the main queue (that would
//    serialise every file in a batch behind AppKit's own work for nothing).
//
// # Apple Event thread strategy (spec §8)
//
// NSAppleScript is documented as main-thread-only, and AESendMessage with
// kAEWaitReply needs a thread whose run loop can service the reply. Go
// goroutines migrate across OS threads freely, so neither is safe to call
// from a goroutine directly. runScriptOnMain() therefore dispatch_async()es
// onto the main queue -- exactly the hop internal/hud already makes for all
// of its AppKit work -- and waits on a semaphore with a hard timeout, so a
// wedged main queue degrades into a reported error instead of a hung deck.
//
// dispatch_sync would have been shorter but deadlocks outright if it ever ran
// on the main thread; async+semaphore cannot, at any call site, now or later.
//
// # ObjC memory
//
// This package is compiled WITHOUT ARC (no -fobjc-arc anywhere in the repo),
// so __block object variables are NOT retained across the block boundary.
// Every value therefore leaves the block as a strdup'd C string -- a plain
// scalar -- and no Objective-C object lifetime ever crosses back to Go.

#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#include <stdlib.h>
#include <string.h>

// Entry separator for the C-string boundary. Deliberately NOT "\n": a
// _kMDItemUserTags entry is itself "Name\nIndex", so joining on newline would
// be ambiguous. 0x1F (ASCII unit separator) cannot occur in a tag name or a
// POSIX path.
static const char kSep = '\x1f';

// appleEventTimeoutSeconds bounds the wait for the main queue. Generous
// enough that a busy Finder still answers, short enough that the serialized
// tag worker cannot be wedged indefinitely by one bad press.
static const double appleEventTimeoutSeconds = 10.0;

// joinWithSep concatenates an NSArray of NSStrings into a freshly strdup'd
// C string separated by kSep. Returns an empty (not NULL) string for an
// empty array -- NULL is reserved for "an error occurred".
static char* joinWithSep(NSArray *items) {
    NSMutableString *acc = [NSMutableString string];
    NSUInteger n = [items count];
    for (NSUInteger i = 0; i < n; i++) {
        id v = [items objectAtIndex:i];
        if (![v isKindOfClass:[NSString class]]) continue;
        if ([acc length] > 0) [acc appendFormat:@"%c", kSep];
        [acc appendString:(NSString *)v];
    }
    const char *utf8 = [acc UTF8String];
    return strdup(utf8 ? utf8 : "");
}

// runScriptOnMain executes an AppleScript source string on the main queue and
// returns its result as a kSep-joined C string (a list result is flattened
// item by item; a plain string result is returned as-is). Returns NULL on
// error with *errOut set to a strdup'd message the caller must free.
static char* runScriptOnMain(NSString *src, char **errOut) {
    __block char *out = NULL;
    __block char *err = NULL;

    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    dispatch_async(dispatch_get_main_queue(), ^{
        @autoreleasepool {
            NSDictionary *errInfo = nil;
            NSAppleScript *script = [[NSAppleScript alloc] initWithSource:src];
            NSAppleEventDescriptor *desc = [script executeAndReturnError:&errInfo];
            if (desc == nil) {
                NSNumber *code = [errInfo objectForKey:NSAppleScriptErrorNumber];
                NSString *msg = [errInfo objectForKey:NSAppleScriptErrorMessage];
                err = strdup([[NSString stringWithFormat:@"AppleScript error %@: %@",
                               code ? code : @"?",
                               msg ? msg : @"(no message)"] UTF8String]);
            } else if ([desc numberOfItems] > 0) {
                NSMutableArray *items = [NSMutableArray array];
                for (NSInteger i = 1; i <= [desc numberOfItems]; i++) {
                    NSString *s = [[desc descriptorAtIndex:i] stringValue];
                    if (s != nil) [items addObject:s];
                }
                out = joinWithSep(items);
            } else {
                NSString *s = [desc stringValue];
                out = strdup(s ? [s UTF8String] : "");
            }
            [script release];
        }
        dispatch_semaphore_signal(sem);
    });

    dispatch_time_t deadline = dispatch_time(DISPATCH_TIME_NOW,
                                             (int64_t)(appleEventTimeoutSeconds * NSEC_PER_SEC));
    if (dispatch_semaphore_wait(sem, deadline) != 0) {
        // The block may still complete later and write through the heap copy
        // of out/err; we simply never read them. A bounded one-off leak on a
        // wedged main queue is the right trade against hanging the worker.
        dispatch_release(sem);
        if (errOut) *errOut = strdup("timed out waiting for the main queue to run the Finder Apple Event");
        return NULL;
    }
    dispatch_release(sem);

    if (out == NULL) {
        if (errOut) *errOut = err ? err : strdup("AppleScript returned no result");
        else if (err) free(err);
        return NULL;
    }
    if (err) free(err);
    return out;
}

// finderSelection returns the POSIX paths of the current Finder selection,
// kSep-joined. Empty string = nothing selected (not an error). NULL = error.
//
// Returns a LIST from AppleScript rather than a delimited string so no text
// delimiter has to be escaped: a path containing the separator is impossible
// because the flattening happens on the descriptor, item by item.
char* finderSelection(char **errOut) {
    NSString *src =
        @"tell application \"Finder\"\n"
        @"  set out to {}\n"
        @"  repeat with anItem in (get selection)\n"
        @"    set end of out to POSIX path of (anItem as alias)\n"
        @"  end repeat\n"
        @"  return out\n"
        @"end tell";
    return runScriptOnMain(src, errOut);
}

// finderProbe sends the cheapest possible Apple Event to Finder purely to
// discover whether the Automation grant is present. Returns 1 on success, 0
// with *errOut set otherwise -- a revoked grant surfaces at launch rather
// than in the middle of a review session (spec §9).
int finderProbe(char **errOut) {
    char *r = runScriptOnMain(@"tell application \"Finder\" to return name", errOut);
    if (r == NULL) return 0;
    free(r);
    return 1;
}

// finderReadTags returns the file's tag NAMES, kSep-joined. No Apple Event --
// a direct NSURL resource-value read. Empty string = untagged. NULL = error.
char* finderReadTags(const char *path, char **errOut) {
    if (path == NULL) {
        if (errOut) *errOut = strdup("nil path");
        return NULL;
    }
    @autoreleasepool {
        NSString *p = [NSString stringWithUTF8String:path];
        if (p == nil) {
            if (errOut) *errOut = strdup("path is not valid UTF-8");
            return NULL;
        }
        NSURL *url = [NSURL fileURLWithPath:p];
        NSError *e = nil;
        id tags = nil;
        if (![url getResourceValue:&tags forKey:NSURLTagNamesKey error:&e]) {
            if (errOut) *errOut = strdup([[e localizedDescription] UTF8String]);
            return NULL;
        }
        if (tags == nil || ![tags isKindOfClass:[NSArray class]]) {
            return strdup(""); // untagged is a normal resting state
        }
        return joinWithSep((NSArray *)tags);
    }
}

// finderWriteTags replaces the file's tag list with joined (kSep-separated;
// empty string clears every tag).
//
// This is the single call the whole design rests on: -setResourceValue:
// forKey:NSURLTagNamesKey has macOS maintain BOTH _kMDItemUserTags and the
// com.apple.FinderInfo colour bits, and clear them consistently. Hand-rolling
// the xattrs instead means writing a binary plist by hand and, on clear,
// removexattr'ing com.apple.FinderInfo -- which destroys the legacy
// Type/Creator codes stored in the same 32-byte blob on older files.
//
// Returns 1 on success, 0 with *errOut set.
int finderWriteTags(const char *path, const char *joined, char **errOut) {
    if (path == NULL) {
        if (errOut) *errOut = strdup("nil path");
        return 0;
    }
    @autoreleasepool {
        NSString *p = [NSString stringWithUTF8String:path];
        if (p == nil) {
            if (errOut) *errOut = strdup("path is not valid UTF-8");
            return 0;
        }
        NSMutableArray *tags = [NSMutableArray array];
        if (joined != NULL && joined[0] != '\0') {
            NSString *j = [NSString stringWithUTF8String:joined];
            if (j == nil) {
                if (errOut) *errOut = strdup("tag list is not valid UTF-8");
                return 0;
            }
            NSString *sep = [NSString stringWithFormat:@"%c", kSep];
            for (NSString *t in [j componentsSeparatedByString:sep]) {
                if ([t length] > 0) [tags addObject:t];
            }
        }
        NSURL *url = [NSURL fileURLWithPath:p];
        NSError *e = nil;
        if (![url setResourceValue:tags forKey:NSURLTagNamesKey error:&e]) {
            if (errOut) *errOut = strdup([[e localizedDescription] UTF8String]);
            return 0;
        }
        return 1;
    }
}
