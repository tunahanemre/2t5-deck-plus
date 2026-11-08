// appIconPNG: the icon of an installed app as a 256x256 PNG, via
// NSWorkspace (Launch Services path lookup, iconForFile). No Screen Recording
// permission, no window, nothing is launched.

#import <AppKit/AppKit.h>
#include <stdlib.h>
#include <string.h>

// Returns malloc'd PNG bytes and sets *outLen, or NULL when the app is not
// installed or rendering fails. Caller frees.
unsigned char* appIconPNG(const char *bundleID, int *outLen) {
    if (bundleID == NULL || outLen == NULL) return NULL;
    *outLen = 0;
    @autoreleasepool {
        NSString *bid = [NSString stringWithUTF8String:bundleID];
        if (bid == nil || [bid length] == 0) return NULL;
        NSWorkspace *ws = [NSWorkspace sharedWorkspace];
        NSURL *url = [ws URLForApplicationWithBundleIdentifier:bid];
        if (url == nil) return NULL;
        NSImage *img = [ws iconForFile:[url path]];
        if (img == nil) return NULL;
        const NSInteger px = 256;
        NSBitmapImageRep *rep = [[NSBitmapImageRep alloc]
            initWithBitmapDataPlanes:NULL pixelsWide:px pixelsHigh:px
            bitsPerSample:8 samplesPerPixel:4 hasAlpha:YES isPlanar:NO
            colorSpaceName:NSCalibratedRGBColorSpace bytesPerRow:0 bitsPerPixel:0];
        if (rep == nil) return NULL;
        // Not ARC: rep came from alloc/init, so every exit below releases it.
        NSGraphicsContext *ctx = [NSGraphicsContext graphicsContextWithBitmapImageRep:rep];
        if (ctx == nil) { [rep release]; return NULL; }
        [NSGraphicsContext saveGraphicsState];
        [NSGraphicsContext setCurrentContext:ctx];
        [img drawInRect:NSMakeRect(0, 0, px, px) fromRect:NSZeroRect
              operation:NSCompositingOperationSourceOver fraction:1.0];
        [NSGraphicsContext restoreGraphicsState];
        NSData *png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
        [rep release];
        if (png == nil || [png length] == 0) return NULL;
        unsigned char *out = malloc([png length]);
        if (out == NULL) return NULL;
        memcpy(out, [png bytes], [png length]);
        *outLen = (int)[png length];
        return out;
    }
}
