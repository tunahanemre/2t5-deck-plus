// The only place a system media key is posted. Called from sendMedia in
// media_send_darwin.go, which has already passed the arm/pause guard.
#import <AppKit/AppKit.h>
#import <CoreGraphics/CoreGraphics.h>

void ldPostMediaKey(long long data1, unsigned long long flags) {
    @autoreleasepool {
        NSEvent *ev = [NSEvent otherEventWithType:NSEventTypeSystemDefined
                                         location:NSMakePoint(0, 0)
                                    modifierFlags:(NSEventModifierFlags)flags
                                        timestamp:0
                                     windowNumber:0
                                          context:nil
                                          subtype:8
                                            data1:(NSInteger)data1
                                            data2:-1];
        CGEventPost(kCGHIDEventTap, [ev CGEvent]);
    }
}
