package ipc

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNotConnected is returned by Send when the client has no live
// connection -- either it was constructed as a zero value, or its last
// Reconnect attempt failed. Callers must never see a net.Conn(nil) panic;
// this is the explicit, checkable signal that a caller (engine.go's
// sidecar recovery, see recoverSidecar) should retry Reconnect rather than
// treat the failure as fatal.
var ErrNotConnected = errors.New("ipc: not connected")

// SendTimeout bounds a single Send round trip (write the request frame +
// read the reply frame). A package var, not a const, so tests can shrink
// it to avoid a real multi-second sleep (same pattern as engine.go's
// sidecarRecoveryBackoff / resolve_driver.py's CONNECT_RETRY_TIMEOUT).
//
// Why this exists (2026-08-12 live incident): before this change, Send had
// NO deadline at all -- a wedged or catastrophically slow sidecar left any
// caller blocked forever. Live evidence: a single "status" round trip
// (expensive on a large timeline -- timecode, full clip map, per-clip
// offline checks) stalled 8.3 SECONDS with nothing else able to run on the
// calling goroutine. When that goroutine was the MIDI callback, CoreMIDI
// queued the presses that arrived during the stall and flushed them all
// within ~1ms once Send finally returned -- a single Space press became
// two, so play+pause cancelled out. queryPage() (engine.go) is now fixed
// to never call into Send from the MIDI callback at all, but Send itself
// still had no bound: ANY other caller (the ticker's own queryStatus, or
// any of the several dispatch* functions that call client.Send directly
// from note_on -- see engine.go's MIDI callback audit) could still hang
// indefinitely against a truly wedged sidecar.
//
// Why 5 seconds: generous relative to a normal round trip (sub-tens-of-
// milliseconds) and to the ~200ms status-cache TTL / 50ms drain tick, so
// it will not fire on a legitimately slow-but-alive sidecar call (the
// "status" op on a large timeline is the slowest op this protocol has, and
// even the live 8.3s incident was very likely a blocked-behind-other-work
// symptom of the bug this fixes, not "status" alone taking that long).
// It's short enough that the absolute worst case -- a sidecar that will
// truly never answer -- becomes a single bounded multi-second stall
// instead of an unbounded one, and it comfortably fits inside
// sidecarRecoveryBackoff (engine.go, 5s): even a tight run of timeouts
// only lets recoverSidecar actually fire a new launchSidecarFn attempt
// once per backoff window, so this can't cause a relaunch storm.
var SendTimeout = 5 * time.Second

// ErrTimeout is returned by Send when a request was fully written but no
// reply arrived within SendTimeout -- DELIBERATELY distinct from a hard
// transport failure (EOF, broken pipe, connection refused, ErrNotConnected).
//
// Why this distinction exists (2026-08-13 live incident, "duplicate sidecar
// feedback loop"): a timeout means the sidecar is SLOW, not dead. Live
// evidence: DaVinci Resolve's own scripting API goes briefly unresponsive
// while PLAYING BACK, and the "status" op's cost also scales with timeline
// size (see engine.go's queryStatus doc comment on the 8.3s stall) -- on the
// user's very large timeline this reliably exceeded SendTimeout a few
// seconds after every Space/C1 press. Before this fix, Send treated a
// timeout exactly like a broken connection: dropLocked() closed it, and the
// caller (engine.go's queryStatus) treated ANY Send failure as "the sidecar
// process died," triggering engine.recoverSidecar -- which spawns a
// replacement sidecar.py on top of the still-alive, still-connected-to-
// Resolve original. sidecar.py's own unconditional os.unlink+bind then
// silently stole the socket out from under the still-running original,
// orphaning it (still alive, still holding a DaVinci Resolve script-
// connection slot, just unreachable) -- and losing that slot made every
// SUBSEQUENT read slower, triggering more timeouts, more recoveries, more
// orphans. Live evidence: three sidecar.py processes found running
// simultaneously, disconnect/reconnect log lines every few minutes.
// See recoverSidecar's doc comment (engine.go) for the REPLACE-not-ADD fix
// on the spawning side; ErrTimeout is the other half, on the read side.
var ErrTimeout = errors.New("ipc: timed out waiting for sidecar reply")

func isTimeoutErr(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// Client is a request/reply connection to the sidecar. Send is one write
// followed by the matching read on a single conn, so two goroutines sharing a
// Client would interleave frames and hand each other's replies back. The mutex
// makes that impossible: callers queue instead of corrupting.
//
// This matters now that a slow op (the timeline repaint) can be dispatched
// from the MIDI goroutine while the 4Hz status poll is running. It matters
// again for Reconnect (BUG 3, 2026-08-12): the sidecar process dying and
// being respawned happens on its own background goroutine (engine.go's
// recoverSidecar), fully concurrently with the MIDI/status goroutines still
// calling Send on this exact *Client -- the SAME mutex that already
// serializes Send against Send also serializes Send against Reconnect
// against Close, so a swap-in-progress can never be observed as a torn
// half-old/half-new read or write.
type Client struct {
	mu   sync.Mutex
	conn net.Conn

	// pendingReply is true when the PREVIOUS Send's read timed out after
	// its request was already fully written -- the sidecar (single-
	// connection, single-threaded, see sidecar.py's serve_connection) is
	// still going to finish that request and write its reply on this same
	// conn, we just gave up waiting. The NEXT Send must drain and discard
	// that stale reply before writing a new request, or its own ReadFrame
	// could return the STALE reply instead of the new one -- see Send's
	// drain step and ErrTimeout's doc comment. Always false when conn is
	// nil (dropLocked/Reconnect both clear it alongside the connection).
	pendingReply bool

	// gate, when set, is asked about every map request's "op" before
	// anything is written; true refuses it (see SetGate).
	gate atomic.Pointer[func(op string) bool]
}

// SetGate installs (nil removes) a refusal gate. A refused Send writes
// nothing and returns {"ok": false, "disarmed": true, "err": ...} with a nil
// error, the shape callers already handle as "the op did not work". Used by
// the engine's safety switch (wave E, F7). Requests that are not a
// map[string]any with a string "op" are never gated.
func (c *Client) SetGate(g func(op string) bool) {
	if g == nil {
		c.gate.Store(nil)
		return
	}
	c.gate.Store(&g)
}

func Dial(sockPath string) (*Client, error) {
	c, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, err
	}
	return &Client{conn: c}, nil
}

func (c *Client) Send(v any) (map[string]any, error) {
	if g := c.gate.Load(); g != nil {
		if m, ok := v.(map[string]any); ok {
			if op, _ := m["op"].(string); op != "" && (*g)(op) {
				return map[string]any{"ok": false, "disarmed": true, "err": "disarmed: " + op + " was not sent"}, nil
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil, ErrNotConnected
	}

	// Drain a reply left over from a PREVIOUS Send whose read timed out
	// (pendingReply's field comment) before sending anything new -- see
	// ErrTimeout's doc comment for why letting the connection survive a
	// read timeout at all is worth this extra step. Bounded by the same
	// SendTimeout: if the stale reply still hasn't shown up, report the
	// timeout again and do NOT send the new request -- the connection
	// stays open either way, so this can never regress into
	// ErrNotConnected on its own; only dropLocked (a genuine transport
	// failure) or Reconnect does that.
	if c.pendingReply {
		if err := c.conn.SetDeadline(time.Now().Add(SendTimeout)); err != nil {
			return nil, err
		}
		if _, err := ReadFrame(c.conn); err != nil {
			if isTimeoutErr(err) {
				return nil, ErrTimeout
			}
			c.dropLocked()
			return nil, err
		}
		c.pendingReply = false
	}

	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	// Deadline covers the WHOLE round trip (write + read), reset fresh on
	// every Send call -- see SendTimeout's doc comment. A no-op cost-wise
	// on the fast path (every real call today completes in low
	// milliseconds); the only thing it changes is what happens when the
	// sidecar is wedged or catastrophically slow: a returned error at
	// SendTimeout instead of blocking the caller forever.
	if err := c.conn.SetDeadline(time.Now().Add(SendTimeout)); err != nil {
		return nil, err
	}
	if err := WriteFrame(c.conn, body); err != nil {
		// A write failure -- of ANY kind, including a timeout -- can leave
		// a PARTIAL frame on the wire: sidecar.py's read_frame is now
		// waiting for bytes that will never arrive. Unlike a read timeout
		// below (the request was already fully, validly sent), there is
		// no safe way to keep using this connection -- always drop it.
		c.dropLocked()
		return nil, err
	}
	reply, err := ReadFrame(c.conn)
	if err != nil {
		if isTimeoutErr(err) {
			// See ErrTimeout's doc comment: the request was already fully
			// written, so this is "busy" (Resolve's scripting API is
			// briefly unresponsive), not "dead". Leave the connection
			// OPEN and remember the reply is still outstanding -- the
			// NEXT Send drains and discards it (see the top of this
			// function) instead of misreading it as its own. The caller
			// (engine.go's queryStatus) decides how many consecutive
			// timeouts justify escalating to a real recovery/respawn.
			c.pendingReply = true
			return nil, ErrTimeout
		}
		c.dropLocked()
		return nil, err
	}
	var out map[string]any
	return out, json.Unmarshal(reply, &out)
}

// dropLocked discards the current connection after a hard Write/ReadFrame
// failure (anything that is NOT a mere read timeout -- see Send's ErrTimeout
// branch, which deliberately does NOT call this) -- caller must already
// hold c.mu (every call site is inside Send, which holds it for the whole
// round trip). A write failure can leave the peer expecting the remainder
// of a frame that will never arrive; any other non-timeout failure (EOF,
// broken pipe, connection refused) means the peer is genuinely gone.
// Nulling c.conn makes every subsequent Send fail fast with ErrNotConnected
// (matching Reconnect's own "no connection" contract, see its doc comment)
// instead of ever risking a misframed read. The caller (engine.go's
// recoverSidecar, triggered by queryStatus's error branch) is expected to
// Reconnect, exactly as it already does for a connection that failed for
// any other reason. Also clears pendingReply -- a reply "outstanding" on a
// connection that no longer exists is meaningless.
func (c *Client) dropLocked() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.pendingReply = false
}

// Reconnect replaces the client's underlying connection with a fresh dial
// to sockPath, closing whatever connection it held before (best-effort --
// a dead peer's Close error is not actionable). The *Client's own identity
// never changes: every existing holder of this pointer (dozens of call
// sites across engine.go, all captured once in Start()) keeps working
// unmodified once the swap completes -- Reconnect is the whole mechanism
// that lets a respawned sidecar process become reachable again without any
// plumbing changes anywhere else.
//
// On dial failure, the client is left with NO connection (conn == nil) --
// not the stale, already-closed one -- so a subsequent Send fails fast
// with ErrNotConnected instead of silently writing into a dead socket or
// panicking on a nil net.Conn. The caller is expected to retry Reconnect
// later (see engine.go's recoverSidecar backoff) rather than treat this as
// fatal.
func (c *Client) Reconnect(sockPath string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.pendingReply = false
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return err
	}
	c.conn = conn
	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}
