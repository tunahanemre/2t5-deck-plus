// hud_darwin.m — native NSPanel status monitor, HUD v2: a WKWebView
// hosting the rich, styled instrument readout (see hud.html), replacing
// v1's plain NSTextField rows.
//
// See internal/hud/hud_darwin.go for the Go-facing API (Show/Hide/Toggle/
// Update) and the arm-guard that keeps `go test` from ever reaching this
// file's AppKit calls.
//
// *** FOCUS SAFETY (the #1 correctness risk, HUD v2's whole reason for a
// spike-first build order) ***
//
// This panel MUST NEVER be able to steal keyboard focus from Resolve, or
// intercept a Loupedeck-driven CGEvent/key-send meant for Resolve. v1's
// plain NSTextFields were already non-interactive (setEditable:NO,
// setSelectable:NO) so this was never a concern; a WKWebView is a much
// heavier-weight NSView that normally participates fully in the responder
// chain (keyboard input, text selection, scrolling) — exactly the
// behavior this HUD (monitor-only, no controls, no input, per the
// package's top-of-file doc comment) must never exhibit.
//
// LDHUDWebView below overrides acceptsFirstResponder and
// becomeFirstResponder to NO — this is the LOAD-BEARING guard. CSS
// `pointer-events`/`user-select` (also applied in hud.html, as hygiene)
// only affects DOM hit-testing inside the web content process; it has NO
// effect on whether the native WKWebView NSView can become the window's
// first responder, so it cannot substitute for the Objective-C override.
// hudPanel additionally sets `setBecomesKeyOnlyIfNeeded:YES` as
// defense-in-depth: even if some future subview change reintroduces a
// control that WANTS key status, the panel only grants it when a subview
// specifically needs it — with LDHUDWebView permanently refusing first
// responder, nothing here ever does.
//
// This mitigation was reviewed and approved as "GO-WITH-MITIGATION" in
// docs/specs/2026-07-06-ld-resolve-hud-rich.md, with an explicit
// requirement that a human-supervised LIVE acceptance test (click/drag in
// the webview, windowed AND Resolve-fullscreen; confirm
// FrontmostBundleID()==Resolve, GateAllow stays true, a real Loupedeck
// action still lands, and CGWindowListCopyWindowInfo shows no new
// kCGWindowLayer==0 entry) runs BEFORE this ships — that test is NOT run
// by this change; see the build report for the exact procedure. If it
// fails, the spec's mandated fallback is v1's restyled-native approach
// (NSTextField rows, already proven safe by the shipped v1 HUD).
//
// Main-thread rule (unchanged from v1): EVERY function below
// dispatch_async()s its AppKit/WebKit work onto the main queue. hudUpdate
// in particular is called from the app's 250ms status-ticker goroutine,
// never the main thread — posting directly from there would be undefined
// behavior for AppKit/WebKit.
//
// String-lifetime note (unchanged pattern from v1): hudShow/hudUpdate
// convert their C-string arguments into NSString objects SYNCHRONOUSLY
// (stringWithUTF8String: copies the bytes at that point) BEFORE returning
// to Go — so hud_darwin.go's `defer C.free`/`C.free` calls, which run the
// instant these functions return, can never race the dispatch_async block
// below, which only ever touches the already-copied NSString objects, not
// the original C buffers.
//
// Lifetime: the panel (and its WKWebView) is a PROCESS-LIFETIME SINGLETON,
// created lazily on first Show() and NEVER released — same locked
// invariant as v1, for the same reason (removes any use-after-free window
// against in-flight dispatch_async blocks that might still be queued when
// the app quits). app.shutdown() intentionally does NOT tear this down.
//
// *** CHANGE 3: white-area + fit-to-content (2026-07-07) ***
//
// A screenshot showed a large opaque WHITE rectangle below the dark HUD
// card. Two independent root causes, both fixed here:
//   (1) setUnderPageBackgroundColor:clear (kept, still needed) only clears
//       the OVERSCROLL/bounce area — WKWebView separately paints an
//       opaque WHITE page background under the whole webview frame unless
//       drawsBackground is explicitly disabled. Fixed via the
//       `[hudWebView setValue:@NO forKey:@"drawsBackground"]` KVC call in
//       _ensurePanel (no public API exists for this).
//   (2) The panel was a FIXED kHUDHeight (284pt) regardless of the
//       actual rendered content's height — any gap between the two
//       showed as exactly that white rectangle (marker-mode/error-state
//       content is shorter). Fixed by making the HUD FIT ITS CONTENT:
//       hud.html's render() measures #hud's offsetHeight after every
//       paint and posts it to native via the "hudResize"
//       WKScriptMessageHandler (LDHUDResizeHandler below), which resizes
//       hudPanel to match, anchored at a fixed TOP-RIGHT point
//       (hudTopRight) so the panel grows/shrinks DOWNWARD rather than
//       drifting.
// All existing focus-safety guards (LDHUDWebView's first-responder
// refusal, becomesKeyOnlyIfNeeded, the drag overlay) are untouched by
// either fix.
//
// *** CHANGE 4: drag snap-back fix (2026-07-08) ***
//
// The fit-to-content resize (LDHUDResizeHandler's applyHeight, added in
// CHANGE 3) re-anchored to the screen-relative hudTopRight point on EVERY
// resize -- so dragging the HUD (via LDHUDDragOverlay, CHANGE from
// 2026-07-07) then turning any knob (which triggers a content-affecting
// status render, hence a resize) snapped the panel straight back to the
// screen's top-right corner, discarding the drag. Fixed by anchoring each
// resize to the panel's OWN current frame (origin.x + current top edge)
// instead -- only the height changes on a resize now; hudTopRight is used
// ONLY for the panel's first-ever frame in _ensurePanel. See applyHeight's
// doc comment for the exact fix.
//
// *** CHANGE 5: virtual-screen placement (2026-07-24) ***
//
// Replaces the ffmpeg/BetterDisplay/VLC CAPTURE chain rejected as too heavy with
// the simplest possible thing: relocate this ALREADY-SHIPPED panel onto a
// BetterDisplay virtual display instead of mainScreen. The virtual display
// (a pre-existing general-purpose
// screen-mirror tool, not built for this feature) is what actually streams
// that screen's pixels to an external controller over WiFi -- this
// file has zero awareness of ffmpeg/VLC/networking, it only decides WHERE
// on-screen the panel draws itself. Zero new process, zero new package,
// zero new sidecar/Resolve API surface.
//
// resolveHUDVirtualScreen() enumerates [NSScreen screens], logging every
// localizedName it sees, and returns the one matching kVirtualScreenName
// ("Virtual 16:9") if present. **CONFIRMED live (2026-07-24, standalone
// Swift script against NSScreen.screens with the virtual display
// connected)**: localizedName IS exactly "Virtual 16:9",
// frame=(-720,-200,720,1280) (720x1280 portrait, as expected), and
// backingScaleFactor=2.0 (the panel gets a 1440x2560 Retina backing store
// automatically -- WKWebView/AppKit handle this from the point-based frame
// with no extra code here). The NSLog inside resolveHUDVirtualScreen stays
// in regardless -- genuinely useful if the name/scale ever changes (a
// BetterDisplay update, a different virtual-display tool) -- tail the
// unified system log while the virtual screen is connected (NSLog goes
// there, NOT the redirected-to-file Go logger -- e.g. `log stream
// --predicate 'process == "2t5-deck-plus"'` or Console.app).
//
// hudComputeFrame() is the SINGLE place deciding the panel's frame: the
// virtual screen's FULL frame (never visibleFrame, which would subtract a
// menu bar/dock a virtual display doesn't have) when resolveHUDVirtualScreen
// finds one, else the ORIGINAL mainScreen/top-right/inset fallback,
// UNCHANGED from before this change. The virtual display is connected/
// disconnected ON DEMAND by the mirror script -- absent is the NORMAL resting
// state, not an error -- so the fallback path is exercised far more often
// in practice, and must never regress (it doesn't: this is the exact same
// computation that ran before this change, just moved into a named
// function so a second call site can reuse it).
//
// hudComputeFrame() is called from two places so they can never drift
// apart: _ensurePanel (the panel's very first frame) and a
// NSApplicationDidChangeScreenParametersNotification observer (registered
// once, lazily, at the end of _ensurePanel, main-queue-dispatched) that
// RELOCATES an already-existing panel the instant the virtual screen
// connects or disconnects -- no relaunch required.
//
// *** FOLLOW-UP FIX INCLUDED (2026-07-24, same day) ***: the pre-existing
// fit-to-content resize path (LDHUDResizeHandler.applyHeight, CHANGE 3
// above) hardcoded width to kHUDWidth (300) and clamped height to at most
// kHUDMaxHeight (900) on EVERY content-driven repaint -- both would have
// fired well inside the 720x1280 the virtual screen was just filled with,
// on the NEXT status tick (<=250ms later, see app/app.go's
// runStatusTicker), silently undoing the fill. Fixed: applyHeight now
// checks resolveHUDVirtualScreen() first and, when present, defers
// unconditionally to hudComputeFrame() (the SAME single source of truth
// used by _ensurePanel and the screen-change observer) instead of running
// its compact fit-to-content sizing at all -- see applyHeight's own doc
// comment for the exact fix. The ordinary mainScreen fallback path (the
// everyday case) is byte-for-byte unchanged.
//
// *** CHANGE 6: menu-bar coverage on the virtual screen (2026-07-24) ***
//
// The user's requirement is an HUD-only controller screen -- no desktop, no
// wallpaper, no menu bar. Wallpaper/stray windows are handled elsewhere
// (floating level already sits above normal windows; hud.html is separately
// gaining an opaque full-bleed background). The remaining gap, confirmed
// live via screen capture: macOS draws an independent menu bar strip at the
// TOP of every NSScreen that owns its own Space (the default "displays have
// separate Spaces" behavior) -- INCLUDING the virtual display -- at
// NSMainMenuWindowLevel (24), which is ABOVE this panel's original
// NSFloatingWindowLevel (3), so the menu bar always won.
//
// Fix: hudLevelForCurrentScreen() (just below hudComputeFrame) returns
// NSStatusWindowLevel (25) -- the LOWEST standard AppKit window-level
// constant that clears NSMainMenuWindowLevel (24) -- ONLY when
// hudOnVirtualScreen is true (a side-effect flag hudComputeFrame sets on
// every call, avoiding a second resolveHUDVirtualScreen() enumeration/NSLog
// burst just to re-ask the same question); NSFloatingWindowLevel is kept
// EXACTLY as before whenever the panel is on the ordinary mainScreen
// fallback -- that is still the daily-driver path, and a panel permanently
// covering the REAL menu bar there would be obnoxious, not requested, and
// explicitly rejected by the coordinator's brief for this change.
//
// Level is set at BOTH places the frame is (re)computed -- _ensurePanel
// (creation) and the screen-change notification handler (relocation) --
// exactly mirroring hudComputeFrame's own two call sites, so level and
// frame can never disagree about which screen the panel is actually on.
// applyHeight's virtual-screen branch deliberately does NOT set level
// itself: it only ever reapplies the SAME frame hudComputeFrame already
// paired with a level moments earlier at one of those two call sites, so
// there is no gap for it to independently correct.
//
// Considered and rejected: a much higher level (e.g. NSScreenSaverWindowLevel,
// 1000, or NSPopUpMenuWindowLevel, 101) would also clear the menu bar, but
// risks sitting above system-level UI (permission prompts, screen-lock,
// alerts) that legitimately expects to be topmost -- unnecessary here since
// NSStatusWindowLevel (one step above the menu bar, the level status-bar-item
// dropdowns themselves use) already clears the ONE specific thing that was
// actually covering the HUD. Window LEVEL is orthogonal to window
// ACTIVATION -- raising it does not touch focus/key-window behavior, so
// LDHUDWebView's first-responder refusal, hudPanel's
// becomesKeyOnlyIfNeeded:YES, and NSWindowStyleMaskNonactivatingPanel all
// remain fully in effect unchanged, on both paths.

#import <AppKit/AppKit.h>
#import <WebKit/WebKit.h>
#import <dispatch/dispatch.h>

// hudOnFixedPlacement is true whenever hudComputeFrame took EITHER the
// virtual-screen or the named-display branch. Declared here, ahead of every
// @implementation that reads it (the drag overlay and applyHeight):
// Objective-C/C requires declaration before use for file-scope statics.
static BOOL hudOnFixedPlacement = NO;
#include <stdlib.h>

// LDHUDWebView is a WKWebView that can NEVER become first responder — see
// this file's top-of-file FOCUS SAFETY comment. This is the load-bearing
// guard; everything else here is ordinary WKWebView usage.
@interface LDHUDWebView : WKWebView
@end

@implementation LDHUDWebView
- (BOOL)acceptsFirstResponder {
    return NO;
}
- (BOOL)becomeFirstResponder {
    return NO;
}
@end

// LDHUDDragOverlay makes the (display-only, always-on-top) HUD panel
// draggable from anywhere on its body.
//
// setMovableByWindowBackground:YES (still set on hudPanel below, as
// defense-in-depth) only fires when a mouseDown reaches the WINDOW without
// being consumed by a subview first -- but hudWebView's WKWebView installs
// its own internal WKContentView subview, which consumes mouseDown itself
// (for potential text selection/scrolling), so the panel background never
// sees the event and never drags. Overriding -mouseDown: on LDHUDWebView
// directly is NOT reliable either: AppKit's front-to-back hit-testing can
// resolve straight to WKContentView (a descendant subview added by WKWebView
// itself) before LDHUDWebView's own override would ever run.
//
// The reliable fix is a transparent overlay view added ON TOP of the whole
// webview (last-added subview = hit-tested FIRST by AppKit's default
// front-to-back -hitTest: traversal), so it intercepts every mouseDown
// before WKContentView gets a chance, and hands it straight to
// performWindowDragWithEvent: -- the standard "drag my window from this
// view" API, safe to call from any view regardless of the window's
// movable-by-background setting. This is safe here SPECIFICALLY because the
// HUD is DISPLAY-ONLY (no interactive controls, no text selection, no links
// -- see this file's top-of-file doc comment): unconditionally swallowing
// every mouse event over the web content costs nothing.
//
// *** FLAGGED FOR LIVE VERIFICATION *** (same convention as the sidecar's
// live-verification flags): performWindowDragWithEvent: on an
// NSWindowStyleMaskNonactivatingPanel is expected to move the window via
// its own drag loop WITHOUT calling any activation path (no
// makeKeyAndOrderFront:, no app activation) -- but this has not been
// exercised against a real window/Resolve session in this change. Confirm
// live: dragging the HUD does NOT flip frontmost away from Resolve and does
// NOT activate this app (FrontmostBundleID() must stay "Resolve" throughout
// the drag, mirroring the same acceptance-test shape already required for
// the WKWebView focus-safety mitigation above).
@interface LDHUDDragOverlay : NSView
@end

@implementation LDHUDDragOverlay
- (BOOL)acceptsFirstResponder {
    return NO;
}
- (BOOL)becomeFirstResponder {
    return NO;
}
- (void)mouseDown:(NSEvent *)event {
    // Fixed placements are FIXED: the strip is pinned to the bottom of a
    // named display and must appear and disappear in exactly that spot. Let
    // it be dragged and it would fight hudComputeFrame, which re-pins it on
    // the next content repaint -- so dragging is simply refused there. The
    // draggable corner card is unaffected.
    if (hudOnFixedPlacement) {
        return;
    }
    [[self window] performWindowDragWithEvent:event];
}
@end

// LDHUDResizeHandler: fit-to-content resize (CHANGE 3) -- full doc comment
// is above its @implementation below, next to the static declarations
// (hudPanel/hudTopRight/kHUDWidth/etc.) its method body references.
// Declared here, separately, only because Objective-C/C requires those
// declarations to precede USE, while @interface itself is order-
// independent.
@interface LDHUDResizeHandler : NSObject <WKScriptMessageHandler>
@end

static NSPanel *hudPanel = nil;
static LDHUDWebView *hudWebView = nil;
static LDHUDDragOverlay *hudDragOverlay = nil;
static LDHUDResizeHandler *hudResizeHandler = nil;
// hudScreenObserverInstalled guards the NSApplicationDidChangeScreenParametersNotification
// registration (CHANGE 5, see this file's top-of-file doc comment) so it is
// added exactly once, lazily, from _ensurePanel -- the same "static BOOL,
// checked once" shape as every other singleton-init step in this file.
static BOOL hudScreenObserverInstalled = NO;
// hudOnVirtualScreen mirrors which branch hudComputeFrame() took on its
// MOST RECENT call (CHANGE 6) -- read by hudLevelForCurrentScreen() so
// callers that need to pair a window LEVEL with whatever frame was just
// computed don't have to call resolveHUDVirtualScreen() a second time
// (which would also double its NSLog output for no reason).
static BOOL hudOnVirtualScreen = NO;

static const CGFloat kHUDWidth = 300.0;
// kHUDHeight is only the INITIAL guess used for the very first frame,
// before hud.html's first render() has a chance to report its real
// content height via the "hudResize" message handler below -- every
// subsequent paint keeps the panel fit exactly to content.
static const CGFloat kHUDHeight = 284.0;
static const CGFloat kHUDInset = 24.0;
// kHUDMinHeight/kHUDMaxHeight sanity-clamp the fit-to-content resize
// against a degenerate (zero/negative, or runaway) reported height --
// defensive only; hud.html's real content is always comfortably within
// this range.
static const CGFloat kHUDMinHeight = 80.0;
static const CGFloat kHUDMaxHeight = 900.0;

// hudTopRight is the panel's INITIAL top-right screen anchor point, used
// ONLY the very first time the panel is created (_ensurePanel, below) --
// NOT by every later resize. See applyHeight's doc comment for why: a
// resize that re-anchored to this fixed screen point on every call was the
// 2026-07-08 "drag snap-back" bug (dragging the HUD, then turning any
// knob -- which triggers a status render/resize -- yanked it right back to
// this corner, discarding the drag).
static NSPoint hudTopRight;

// Forward declarations (CHANGE 5, 2026-07-24): resolveHUDVirtualScreen and
// hudComputeFrame are DEFINED further down (right before _ensurePanel,
// their main use site), but LDHUDResizeHandler's applyHeight (just below)
// needs to call both -- Objective-C/C requires declaration before use, the
// same reason this file's @interface/@implementation split already exists
// (see the doc comment on LDHUDResizeHandler's own @interface above).
// hudOnFixedPlacement is true whenever hudComputeFrame took EITHER the
// virtual-screen or the named-display branch. applyHeight keys off THIS, not
// hudOnVirtualScreen: deferring only for the virtual screen would let the
// fit-to-content resize reclamp a 3840-wide strip back to a 300pt card on the
// very next status tick (~250ms) -- the exact regression fixed on 2026-07-24,
// reintroduced. Flagged by review before it shipped.

static NSScreen *resolveHUDVirtualScreen(void);
static NSRect hudComputeFrame(void);

// LDHUDResizeHandler receives "hudResize" postMessage calls from hud.html's
// render() (see that file's postHeight()), carrying the just-painted
// content's pixel height (message.body, a JS number -> NSNumber here), and
// resizes hudPanel to fit -- see this file's top-of-file doc comment's
// CHANGE 3 note (the drawsBackground/fixed-height white-area fix).
//
// *** FIXED 2026-07-08: drag snap-back ***. This used to re-anchor to the
// fixed, screen-relative hudTopRight point on EVERY resize -- but a resize
// fires on every content-affecting render (roughly every status tick while
// anything changes, which in practice is "whenever the user turns a
// knob"), so any drag the user had just done was silently discarded the
// next time a knob moved, snapping the panel straight back to the top-right
// corner of the screen. Fixed by anchoring each resize to the panel's OWN
// CURRENT frame instead (read via [hudPanel frame], already captured below
// as `current`): origin.x and the current TOP edge (origin.y + current
// height) are preserved exactly, and only the height changes -- the panel
// grows/shrinks downward from wherever the user last left it. hudTopRight
// is now consulted ONLY by _ensurePanel, for the panel's very first frame
// (before the user has ever had a chance to drag it).
//
// WKScriptMessageHandler's didReceiveScriptMessage: is guaranteed by
// WebKit to run on the main thread already (unlike hudShow/hudUpdate,
// which are called from Go's 250ms status-ticker goroutine and must
// dispatch_async onto main themselves) -- no additional main-thread hop
// needed here.
@implementation LDHUDResizeHandler
- (void)userContentController:(WKUserContentController *)userContentController
        didReceiveScriptMessage:(WKScriptMessage *)message {
    [self applyHeight:message.body];
}

// applyHeight is a plain (non-WebKit-callback) method so tests/future
// callers could exercise the clamping/no-op logic without a real
// WKScriptMessage -- not currently exercised (this package is arm-guarded
// with zero Go test files, matching v1/v2, see internal/hud's package doc
// comment), but keeping the logic out of the delegate method itself costs
// nothing and matches this file's general style of small, single-purpose
// functions.
- (void)applyHeight:(id)body {
    if (hudPanel == nil) {
        return;
    }
    if (![body isKindOfClass:[NSNumber class]]) {
        return;
    }

    // CHANGE 5 fix (2026-07-24): on the virtual screen, hudComputeFrame()
    // is the SINGLE source of truth for the panel's frame (fills the whole
    // 720x1280 edge-to-edge) -- the fit-to-content sizing below (compact
    // kHUDWidth/kHUDMinHeight/kHUDMaxHeight) is a mainScreen-only concept
    // for the small draggable card, and MUST NOT fight the fill by
    // re-clamping the panel back down to that compact size on every
    // content-driven repaint (which is what shipping this file's original
    // CHANGE 5 without this fix would have done -- the panel would have
    // snapped back within one status tick, ~250ms). Defer to
    // hudComputeFrame() unconditionally whenever the virtual screen is
    // present; `body`'s reported content height is simply irrelevant while
    // docked there. The ordinary mainScreen fallback path below (the
    // everyday case) is untouched.
    //
    // GENERALISED 2026-07-29: this check was `resolveHUDVirtualScreen() != nil`,
    // i.e. virtual-screen ONLY. A named-display strip would have fallen
    // straight through to the compact branch below and been reclamped from
    // 3840x320 to a 300pt card within one status tick -- reintroducing the
    // very regression the paragraph above describes. It now defers for ANY
    // fixed placement. hudComputeFrame() is called FIRST so its
    // hudOnFixedPlacement side effect reflects the CURRENT screen layout
    // rather than whatever the last call left behind.
    NSRect fixedFrame = hudComputeFrame();
    if (hudOnFixedPlacement) {
        if (!NSEqualRects([hudPanel frame], fixedFrame)) {
            [hudPanel setFrame:fixedFrame display:YES];
        }
        return;
    }

    CGFloat height = [(NSNumber *)body doubleValue];
    if (height < kHUDMinHeight) {
        height = kHUDMinHeight;
    }
    if (height > kHUDMaxHeight) {
        height = kHUDMaxHeight;
    }
    NSRect current = [hudPanel frame];
    if (fabs(current.size.height - height) < 0.5) {
        return; // no meaningful change -- avoid redundant setFrame churn
    }
    // Anchor to the panel's CURRENT top-left corner, not the screen-relative
    // hudTopRight (see this method's + hudTopRight's doc comments above for
    // the drag-snap-back bug this fixes) -- only the height changes here,
    // so the panel grows/shrinks downward from wherever the user last
    // dragged it, instead of jumping back to the screen's top-right corner.
    CGFloat topEdge = current.origin.y + current.size.height;
    NSRect newFrame = NSMakeRect(current.origin.x, topEdge - height, kHUDWidth, height);
    [hudPanel setFrame:newFrame display:YES];
}
@end

// kVirtualScreenName is the BetterDisplay virtual display's localizedName,
// matched exactly (case-sensitive) by resolveHUDVirtualScreen below.
// CONFIRMED live 2026-07-24 (NSScreen.screens against a connected virtual
// display) -- see this file's CHANGE 5 doc comment for the exact captured
// output.
static NSString * const kVirtualScreenName = @"Virtual 16:9";

// resolveHUDVirtualScreen enumerates every currently-connected NSScreen,
// logging each one's localizedName + frame (the live-spike observability
// CHANGE 5 asked for -- this runs every time hudComputeFrame runs, i.e. on
// initial panel creation AND on every screen-configuration change, not
// just once), and returns the one matching kVirtualScreenName, or nil if
// none does. nil is the NORMAL/common case (the virtual display is
// connected/disconnected on demand by the mirror script), never treated as
// an error by any caller.
static NSScreen *resolveHUDVirtualScreen(void) {
    NSArray<NSScreen *> *screens = [NSScreen screens];
    NSScreen *found = nil;
    for (NSScreen *s in screens) {
        NSLog(@"[hud] screen: localizedName=%@ frame=%@",
              [s localizedName], NSStringFromRect([s frame]));
        if (found == nil && [[s localizedName] isEqualToString:kVirtualScreenName]) {
            found = s;
        }
    }
    return found;
}

// hudComputeFrame is the ONE place that decides the HUD panel's frame --
// see this file's CHANGE 5 doc comment for the full rationale. Called from
// both _ensurePanel (the panel's very first frame) and the
// NSApplicationDidChangeScreenParametersNotification handler below
// (relocating an EXISTING panel) so the two call sites can never drift out
// of sync. Also updates the shared hudTopRight static -- consulted only by
// _ensurePanel's very-first-frame path, unchanged from CHANGE 4.
// ---------------------------------------------------------------------------
// Fixed "display" placement (lower third)
// ---------------------------------------------------------------------------
//
// A third placement alongside the virtual screen and the mainScreen corner:
// a full-width strip pinned to an edge of a NAMED display, at a fixed height.
// Built for a 3840x2160 UNSCALED panel where the 300pt corner card is
// unreadably small.
//
// Configured from Go via hudSetPlacement(). Absent/unmatched config is NOT an
// error -- it simply falls through to the existing behaviour, the same way a
// missing virtual screen does. A monitor being unplugged is a normal resting
// state, not a failure.
static NSString *hudDisplayName = nil;   // substring matched against localizedName
static NSString *hudEdge        = nil;   // "bottom" (default) or "top"
static CGFloat   hudFixedHeight = 0;     // 0 == placement inactive

void hudSetPlacement(const char *displayName, const char *edge, double heightPx) {
    hudDisplayName = (displayName && *displayName) ? [NSString stringWithUTF8String:displayName] : nil;
    hudEdge        = (edge && *edge) ? [NSString stringWithUTF8String:edge] : @"bottom";
    hudFixedHeight = (CGFloat)heightPx;
    NSLog(@"[hud] placement display=%@ edge=%@ height=%.0f",
          hudDisplayName ?: @"(none)", hudEdge, hudFixedHeight);
}

// resolveHUDNamedScreen returns the screen whose localizedName CONTAINS
// hudDisplayName (case-insensitive), or nil.
//
// Substring rather than exact match so a partial name like "PB287Q" works
// against the reported "ASUS PB287Q". localizedName is EDID-derived and
// stable across reconnect/sleep/reboot, unlike CGDirectDisplayID.
//
// First match wins. With two identical monitors that is unpredictable, since
// NSScreen.screens order is not guaranteed -- so log when more than one
// matches rather than silently picking.
static NSScreen *resolveHUDNamedScreen(void) {
    if (hudDisplayName == nil || hudFixedHeight <= 0) { return nil; }
    NSScreen *found = nil;
    NSUInteger matches = 0;
    for (NSScreen *s in [NSScreen screens]) {
        NSString *name = [s localizedName];
        if (name && [name rangeOfString:hudDisplayName
                                options:NSCaseInsensitiveSearch].location != NSNotFound) {
            matches++;
            if (found == nil) { found = s; }
        }
    }
    if (matches > 1) {
        NSLog(@"[hud] WARNING: %lu screens match %@ -- using the first; NSScreen order is not guaranteed",
              (unsigned long)matches, hudDisplayName);
    }
    return found;
}

static NSRect hudComputeFrame(void) {
    hudOnFixedPlacement = NO;
    NSScreen *virtualScreen = resolveHUDVirtualScreen();
    hudOnVirtualScreen = (virtualScreen != nil);
    if (virtualScreen != nil) {
        hudOnFixedPlacement = YES;
        // Fill the virtual display's FULL frame (not visibleFrame -- a
        // virtual display has no menu bar/dock to exclude), expected to be
        // 720x1280 portrait per the mirror script's configured resolution
        // (read dynamically here, never hardcoded, so a future resolution
        // change doesn't silently mis-size the panel).
        NSRect frame = [virtualScreen frame];
        hudTopRight = NSMakePoint(NSMaxX(frame), NSMaxY(frame));
        return frame;
    }
    // Named-display strip (lower third). Precedence is deliberate:
    // virtual screen > named display > mainScreen corner. The virtual screen
    // feeds the external controller during ACTIVE use via
    // the mirror script; the named display is a passive glance monitor, so the
    // active tool wins.
    NSScreen *named = resolveHUDNamedScreen();
    if (named != nil) {
        hudOnFixedPlacement = YES;
        // visibleFrame, NOT frame -- unlike the virtual-screen branch above,
        // which deliberately uses frame because a virtual display has no menu
        // bar or Dock to subtract. Do not unify these two: they differ on
        // purpose. On the measured PB287Q visibleFrame == frame anyway (macOS
        // reserves no menu bar there), but visibleFrame stays correct if the
        // Dock ever migrates to it.
        NSRect vf = [named visibleFrame];
        CGFloat h = hudFixedHeight;
        if (h > vf.size.height) { h = vf.size.height; }
        CGFloat y = [hudEdge isEqualToString:@"top"] ? (NSMaxY(vf) - h) : vf.origin.y;
        NSRect strip = NSMakeRect(vf.origin.x, y, vf.size.width, h);
        hudTopRight = NSMakePoint(NSMaxX(strip), NSMaxY(strip));
        return strip;
    }

    // Fallback: no virtual screen currently present -- this is the exact
    // ORIGINAL mainScreen/top-right/inset computation, unchanged.
    NSScreen *screen = [NSScreen mainScreen];
    NSRect screenFrame = screen ? [screen visibleFrame] : NSMakeRect(0, 0, 1440, 900);
    hudTopRight = NSMakePoint(NSMaxX(screenFrame) - kHUDInset, NSMaxY(screenFrame) - kHUDInset);
    return NSMakeRect(
        hudTopRight.x - kHUDWidth,
        hudTopRight.y - kHUDHeight,
        kHUDWidth, kHUDHeight);
}

// hudLevelForCurrentScreen returns the window level to pair with whatever
// frame hudComputeFrame() most recently computed (CHANGE 6) -- MUST be
// called AFTER hudComputeFrame() in the same pass (reads its
// hudOnVirtualScreen side effect rather than re-resolving the screen
// itself). NSStatusWindowLevel (25) is the lowest standard AppKit level
// that clears NSMainMenuWindowLevel (24) -- see this file's CHANGE 6
// top-of-file doc comment for the full rationale, including why a higher
// level was considered and rejected. NSFloatingWindowLevel (3) is returned
// unchanged for the ordinary mainScreen path -- the daily driver, where
// covering the real menu bar was never wanted.
// Clean Feed:
// Resolve's Clean Feed is a borderless window at NSScreenSaverWindowLevel (measured live:
// kCGWindowLayer = 1000, 3840x2160 on the PB287Q) and it covered the HUD (layer 3). On a
// NAMED fixed display (bindings.json hud.display) the HUD now sits one step above it.
// The mainScreen fallback stays floating, and the virtual screen stays on the status level.
static NSWindowLevel hudLevelForCurrentScreen(void) {
    if (hudOnVirtualScreen) return NSStatusWindowLevel;
    if (hudOnFixedPlacement) return NSScreenSaverWindowLevel + 1;
    return NSFloatingWindowLevel;
}

// _ensurePanel lazily builds the singleton NSPanel + its WKWebView, loading
// `html` (the caller's already-converted NSString, never re-parsed from a
// C pointer here) exactly once. MUST be called only from the main thread —
// every call site below already hops onto the main queue via dispatch_async
// before calling this.
static void _ensurePanel(NSString *html) {
    if (hudPanel != nil) {
        return;
    }

    NSRect frame = hudComputeFrame();

    hudPanel = [[NSPanel alloc] initWithContentRect:frame
                                           styleMask:(NSWindowStyleMaskNonactivatingPanel | NSWindowStyleMaskBorderless)
                                             backing:NSBackingStoreBuffered
                                               defer:NO];
    // CHANGE 6: NSStatusWindowLevel on the virtual screen (clears the
    // per-screen menu bar drawn there); NSFloatingWindowLevel, exactly as
    // before, everywhere else -- see hudLevelForCurrentScreen's doc
    // comment. Must run AFTER hudComputeFrame() above (reads its
    // hudOnVirtualScreen side effect).
    [hudPanel setLevel:hudLevelForCurrentScreen()];
    [hudPanel setCollectionBehavior:(NSWindowCollectionBehaviorCanJoinAllSpaces |
                                      NSWindowCollectionBehaviorStationary |
                                      NSWindowCollectionBehaviorFullScreenAuxiliary)];
    [hudPanel setHidesOnDeactivate:NO];
    [hudPanel setMovableByWindowBackground:YES];
    [hudPanel setOpaque:NO];
    [hudPanel setBackgroundColor:[NSColor clearColor]];
    // No shadow on a full-bleed fixed placement -- a native drop shadow along
    // the edge of an edge-to-edge strip is floating-card chrome that fights
    // the deliberate border:none/box-shadow:none the wide CSS applies.
    [hudPanel setHasShadow:(hudOnFixedPlacement ? NO : YES)];
    [hudPanel setReleasedWhenClosed:NO];
    // Defense-in-depth (see top-of-file FOCUS SAFETY comment): even though
    // LDHUDWebView already permanently refuses first responder, this
    // ensures the panel itself only ever becomes key if some subview
    // specifically needs it -- with no such subview, it simply never does.
    [hudPanel setBecomesKeyOnlyIfNeeded:YES];

    WKWebViewConfiguration *webCfg = [[WKWebViewConfiguration alloc] init];
    // "hudResize" message handler (CHANGE 3, fit-to-content): registered
    // on the configuration's userContentController BEFORE the webview
    // itself is initialized below -- hud.html's script calls
    // window.webkit.messageHandlers.hudResize.postMessage(...) at the end
    // of every render(), see that file.
    hudResizeHandler = [[LDHUDResizeHandler alloc] init];
    [webCfg.userContentController addScriptMessageHandler:hudResizeHandler name:@"hudResize"];

    NSRect webFrame = NSMakeRect(0, 0, kHUDWidth, kHUDHeight);
    hudWebView = [[LDHUDWebView alloc] initWithFrame:webFrame configuration:webCfg];
    [hudWebView setAutoresizingMask:(NSViewWidthSizable | NSViewHeightSizable)];
    // Let the panel's own rounded corners/shadow show through -- hud.html's
    // <html>/<body> are already background:transparent; this is the native
    // half of that (WKWebView defaults to an opaque white page background
    // otherwise).
    if (@available(macOS 12.3, *)) {
        [hudWebView setUnderPageBackgroundColor:[NSColor clearColor]];
    }
    // CHANGE 3 (white-area fix, part 1 of 2): setUnderPageBackgroundColor:
    // above only clears the OVERSCROLL/bounce area -- WKWebView still
    // paints its own opaque WHITE page background underneath the
    // transparent HTML body unless drawsBackground is explicitly
    // disabled. There is no public API for this (WKWebView predates a
    // public property for it); the KVC path below is the standard,
    // widely-used way to reach the underlying flag. Without this, any
    // area of the fixed-size WKWebView frame not covered by hud.html's
    // actual rendered content (e.g. the panel's old fixed 284pt height
    // vs. a shorter card) painted as a large opaque white rectangle
    // instead of showing the (already setOpaque:NO) panel behind it.
    [hudWebView setValue:@NO forKey:@"drawsBackground"];
    [hudPanel setContentView:hudWebView];
    [hudWebView loadHTMLString:(html ?: @"") baseURL:nil];

    // Added AFTER hudWebView's own internal subviews exist (WKWebView builds
    // those during -initWithFrame:configuration: above), so this is last in
    // hudWebView's subviews array -- and therefore hit-tested FIRST -- making
    // the whole panel draggable from anywhere on its body. See
    // LDHUDDragOverlay's doc comment above for why an overlay is used
    // instead of overriding -mouseDown: on LDHUDWebView directly.
    hudDragOverlay = [[LDHUDDragOverlay alloc] initWithFrame:webFrame];
    [hudDragOverlay setAutoresizingMask:(NSViewWidthSizable | NSViewHeightSizable)];
    [hudWebView addSubview:hudDragOverlay];

    // CHANGE 5: relocate the panel live when the virtual screen connects/
    // disconnects (the mirror script does this on demand, not this app) --
    // registered exactly once, lazily, here (guarded by
    // hudScreenObserverInstalled, the same "static BOOL checked once"
    // shape as this whole function's own hudPanel != nil guard).
    // queue:[NSOperationQueue mainQueue] makes the block's hudPanel/
    // hudComputeFrame access main-thread-certain rather than assumed (the
    // notification is already posted on main in practice, but every other
    // AppKit touch in this file is explicit about the main-thread rule --
    // see the top-of-file doc comment -- so this matches that discipline).
    if (!hudScreenObserverInstalled) {
        hudScreenObserverInstalled = YES;
        [[NSNotificationCenter defaultCenter]
            addObserverForName:NSApplicationDidChangeScreenParametersNotification
                        object:nil
                         queue:[NSOperationQueue mainQueue]
                    usingBlock:^(NSNotification * _Nonnull note) {
            if (hudPanel == nil) {
                return;
            }
            NSRect newFrame = hudComputeFrame();
            // CHANGE 6: re-pair the level with whatever screen the panel
            // just relocated to -- same hudLevelForCurrentScreen() call as
            // _ensurePanel, must run AFTER hudComputeFrame() immediately
            // above (reads its hudOnVirtualScreen side effect).
            [hudPanel setLevel:hudLevelForCurrentScreen()];
            [hudPanel setFrame:newFrame display:YES];
        }];
    }
}

// hudShow orders the singleton panel to the front (creating it, and
// loading `html` into its WKWebView, on first call). orderFrontRegardless
// (not makeKeyAndOrderFront:) never activates the app or steals key/focus
// from Resolve -- required by NSWindowStyleMaskNonactivatingPanel's whole
// purpose, and doubly reinforced here by LDHUDWebView's first-responder
// refusal + becomesKeyOnlyIfNeeded above.
void hudShow(const char* html) {
    NSString *htmlStr = [NSString stringWithUTF8String:(html ? html : "")];
    dispatch_async(dispatch_get_main_queue(), ^{
        _ensurePanel(htmlStr);
        [hudPanel orderFrontRegardless];
    });
}

// hudHide orders the panel out (hides it) WITHOUT closing/releasing it --
// see this file's top-of-file lifetime note.
void hudHide(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (hudPanel != nil) {
            [hudPanel orderOut:nil];
        }
    });
}

// hudUpdate pushes a new state JSON object into the web content by
// evaluating "window.hud.render(<json>)" -- window.hud.render is defined in
// hud.html's <script> and does DOM-binding only (see that file). Passing
// valid JSON directly as a JavaScript expression is safe: JSON is a
// (near-)strict subset of JS object/array literal syntax under the
// ECMAScript 2019+ grammar that JavaScriptCore (WKWebView's JS engine) has
// implemented since 2018, so no additional escaping/JSON.parse wrapping is
// needed. `_ensurePanel(nil)` here defensively creates the panel with an
// EMPTY page if Update somehow raced ahead of the first Show() -- see this
// file's top-of-file string-lifetime note for why the NSString conversion
// happens BEFORE the dispatch_async block, not inside it.
void hudUpdate(const char* stateJSON) {
    NSString *json = [NSString stringWithUTF8String:(stateJSON ? stateJSON : "{}")];
    dispatch_async(dispatch_get_main_queue(), ^{
        _ensurePanel(nil);
        if (hudWebView == nil) {
            return;
        }
        NSString *js = [NSString stringWithFormat:@"window.hud && window.hud.render(%@);", json];
        [hudWebView evaluateJavaScript:js completionHandler:nil];
    });
}
