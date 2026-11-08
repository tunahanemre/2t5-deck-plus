package ipc

// Tests for BUG 3 (2026-08-12 live report): the sidecar process dies
// whenever DaVinci Resolve is restarted, and never comes back until the
// whole app is relaunched -- confirmed live (Resolve's PID newer than the
// app's, sidecar process gone, an engine restart did NOT respawn it).
// engine.go's July self-heal (_ensure_connected, sidecar-side) covers a
// dead CONNECTION; it does nothing for the sidecar PROCESS itself being
// gone, since nothing ever re-dials once the initial Start()-time dial
// succeeds.
//
// This file covers the low-level primitive the engine-level fix (see
// internal/engine/engine.go's recoverSidecar) needs: reconnecting an
// EXISTING *Client's underlying connection in place, so its identity
// (the pointer) never changes -- every call site across engine.go that
// already holds a *ipc.Client keeps working unmodified once the
// connection behind it is swapped, with no plumbing changes anywhere
// else. Send/Reconnect/Close must all be safe to call concurrently with
// each other (Send is already called from more than one engine goroutine
// today) and Send on a client with no live connection must return a
// clear error, never panic on a nil net.Conn.

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// startEchoServer listens on a fresh unix socket and replies {"ok": true,
// "from": tag} to every request it receives -- lets a test tell which of
// two (or more) servers a given Send() actually reached.
func startEchoServer(t *testing.T, tag string) (sockPath string, closeServer func()) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-ipc-test-*.sock")
	if err != nil {
		t.Fatalf("tempname: %v", err)
	}
	sockPath = f.Name()
	f.Close()
	os.Remove(sockPath)
	t.Cleanup(func() { os.Remove(sockPath) })

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					body, err := ReadFrame(c)
					if err != nil {
						return
					}
					_ = body
					reply, _ := json.Marshal(map[string]any{"ok": true, "from": tag})
					if err := WriteFrame(c, reply); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	return sockPath, func() { ln.Close() }
}

func TestClient_Send_NoConnection_ReturnsErrNotConnected_NotPanic(t *testing.T) {
	c := &Client{} // zero value: conn is nil, never dialed
	resp, err := c.Send(map[string]any{"op": "ping"})
	if err == nil {
		t.Fatal("Send with no connection: expected an error, got nil")
	}
	if resp != nil {
		t.Errorf("Send with no connection: expected nil response, got %v", resp)
	}
}

func TestClient_Dial_ThenSend_Works(t *testing.T) {
	sockPath, closeServer := startEchoServer(t, "A")
	defer closeServer()

	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	resp, err := c.Send(map[string]any{"op": "ping"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp["from"] != "A" {
		t.Errorf("resp[from] = %v, want A", resp["from"])
	}
}

func TestClient_Reconnect_SwapsToNewConnection(t *testing.T) {
	sockA, closeA := startEchoServer(t, "A")
	defer closeA()
	sockB, closeB := startEchoServer(t, "B")
	defer closeB()

	c, err := Dial(sockA)
	if err != nil {
		t.Fatalf("Dial A: %v", err)
	}
	defer c.Close()

	resp, err := c.Send(map[string]any{"op": "ping"})
	if err != nil {
		t.Fatalf("Send (pre-reconnect): %v", err)
	}
	if resp["from"] != "A" {
		t.Fatalf("resp[from] = %v, want A before reconnect", resp["from"])
	}

	if err := c.Reconnect(sockB); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	resp, err = c.Send(map[string]any{"op": "ping"})
	if err != nil {
		t.Fatalf("Send (post-reconnect): %v", err)
	}
	if resp["from"] != "B" {
		t.Errorf("resp[from] = %v, want B after Reconnect -- the SAME *Client "+
			"must now be talking to the new server", resp["from"])
	}
}

func TestClient_Reconnect_ClosesThePriorConnection(t *testing.T) {
	sockA, closeA := startEchoServer(t, "A")
	defer closeA()
	sockB, closeB := startEchoServer(t, "B")
	defer closeB()

	c, err := Dial(sockA)
	if err != nil {
		t.Fatalf("Dial A: %v", err)
	}
	defer c.Close()

	oldConn := c.conn // white-box: same package, allowed to peek at the field
	if err := c.Reconnect(sockB); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	// The OLD connection must have been closed by Reconnect, not merely
	// abandoned (which would leak the fd for the process's lifetime).
	// A closed net.Conn returns an error on any further I/O.
	oldConn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := oldConn.Read(buf); err == nil {
		t.Error("old connection is still readable after Reconnect -- it was not closed")
	}
}

func TestClient_Reconnect_FailureLeavesClientUsableAndClearlyDisconnected(t *testing.T) {
	sockPath, closeServer := startEchoServer(t, "A")
	defer closeServer()

	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	deadPath := t.TempDir() + "/nothing-listening-here.sock"
	if err := c.Reconnect(deadPath); err == nil {
		t.Fatal("Reconnect to a dead path: expected an error, got nil")
	}

	// After a FAILED reconnect, the client must not silently keep using
	// the (now explicitly closed, by Reconnect's own contract) prior
	// connection, and must not panic on the next Send -- it must report
	// itself as not connected so the caller (engine.go's recoverSidecar)
	// knows to retry rather than believing it's still talking to the old
	// server.
	_, err = c.Send(map[string]any{"op": "ping"})
	if err == nil {
		t.Error("Send after a failed Reconnect: expected an error (not silently using a stale connection), got nil")
	}
}

// ---------------------------------------------------------------------------
// SendTimeout (2026-08-12 root-cause fix): Send must never block forever.
// Before this, a wedged or catastrophically slow sidecar left ANY caller
// blocked indefinitely -- live evidence: a single "status" round trip
// stalled 8.3 seconds, and when it was the MIDI-callback goroutine
// waiting, CoreMIDI queued the presses that arrived during the stall and
// flushed them all within ~1ms once Send finally returned (a single Space
// press became two -- play+pause, cancelling out). These tests cover the
// primitive directly; internal/engine's
// TestNoteOnDispatchesPromptly_WhenSidecarStatusIsSlow covers the same
// contract end to end through the real MIDI-callback path.
// ---------------------------------------------------------------------------

// startHangingServer accepts connections and then does nothing at all --
// never reads, never writes, never closes -- simulating a sidecar process
// that is wedged (accepted the TCP/unix handshake but is not servicing
// its accept loop, e.g. deadlocked). WriteFrame from the client side can
// still succeed (the request fits in the kernel's socket send buffer with
// no reader required), so this specifically exercises the READ-side
// deadline -- Send waiting forever for a reply that will never come.
func startHangingServer(t *testing.T) (sockPath string) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-hang-*.sock")
	if err != nil {
		t.Fatalf("tempname: %v", err)
	}
	sockPath = f.Name()
	f.Close()
	os.Remove(sockPath)
	t.Cleanup(func() { os.Remove(sockPath) })

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Deliberately leaked for the test's lifetime: this goroutine
			// just holds the connection open and does nothing else. Cleanup
			// closes the listener; the process exiting reclaims the rest.
			_ = conn
		}
	}()
	return sockPath
}

// shrinkSendTimeout replaces the package-level SendTimeout var with a tiny
// value for the duration of a test -- same pattern as engine.go's
// sidecarRecoveryBackoff / resolve_driver.py's CONNECT_RETRY_TIMEOUT (both
// documented as package/instance vars specifically so tests don't pay a
// real multi-second sleep).
func shrinkSendTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := SendTimeout
	SendTimeout = d
	t.Cleanup(func() { SendTimeout = orig })
}

func TestClient_Send_TimesOutRatherThanHangingForever(t *testing.T) {
	shrinkSendTimeout(t, 30*time.Millisecond)

	sockPath := startHangingServer(t)
	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	start := time.Now()
	_, err = c.Send(map[string]any{"op": "ping"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Send against a wedged sidecar: expected a timeout error, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Send against a wedged sidecar took %v (SendTimeout=30ms) -- it must never block indefinitely", elapsed)
	}
}

// TestClient_Send_ReadTimeout_ReturnsErrTimeout_ConnectionStaysOpen documents
// the 2026-08-13 policy change (superseding the old
// TestClient_Send_AfterTimeout_ConnectionIsDroppedNotPoisoned contract this
// test replaces): a read timeout no longer drops the connection. Live
// incident: engine.go used to treat a timed-out Send exactly like a broken
// connection, and its caller (queryStatus) treated ANY Send failure as "the
// sidecar died" -- spawning a REPLACEMENT sidecar.py on top of a merely-busy
// (not dead) original every time DaVinci Resolve's scripting API got briefly
// slow during playback. See ErrTimeout's doc comment (client.go) for the
// full incident. The connection must now stay open across a read timeout --
// Reconnect/dropLocked (a genuine transport failure) are the only things
// that may still tear it down.
func TestClient_Send_ReadTimeout_ReturnsErrTimeout_ConnectionStaysOpen(t *testing.T) {
	shrinkSendTimeout(t, 30*time.Millisecond)

	sockPath := startHangingServer(t)
	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	if _, err := c.Send(map[string]any{"op": "ping"}); !errors.Is(err, ErrTimeout) {
		t.Fatalf("Send against a wedged sidecar: err = %v, want ErrTimeout", err)
	}

	// White-box: the connection itself must not have been closed/nilled by
	// the timeout -- c.conn is only cleared by dropLocked/Reconnect, neither
	// of which the ErrTimeout branch calls.
	c.mu.Lock()
	stillConnected := c.conn != nil
	pending := c.pendingReply
	c.mu.Unlock()
	if !stillConnected {
		t.Error("Send after a read timeout closed the connection -- it must stay open (see ErrTimeout's doc comment)")
	}
	if !pending {
		t.Error("Send after a read timeout did not mark pendingReply -- the next Send would not know to drain the stale reply")
	}
}

// TestClient_Send_RepeatedTimeouts_KeepsReturningErrTimeout_NeverPanics
// proves a GENUINELY wedged sidecar (never replies, ever) degrades to a
// repeatable ErrTimeout on every Send, never silently flips to
// ErrNotConnected or panics on its own. This primitive never decides
// anything on its own about how long is too long -- that judgment belongs
// entirely to the caller. As of 2026-08-25 (engine.go's queryStatus, third
// attempt at this bug class after a counter and then a same-process ping
// probe both failed) the caller's answer is: never escalate on ErrTimeout
// alone, no matter the count or duration -- only call Reconnect in response
// to a hard transport failure. The client primitive itself must never
// unilaterally declare the connection dead just because it's slow.
func TestClient_Send_RepeatedTimeouts_KeepsReturningErrTimeout_NeverPanics(t *testing.T) {
	shrinkSendTimeout(t, 15*time.Millisecond)

	sockPath := startHangingServer(t)
	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	for i := 0; i < 5; i++ {
		_, err := c.Send(map[string]any{"op": "ping", "i": i})
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("Send #%d against a permanently wedged sidecar: err = %v, want ErrTimeout", i, err)
		}
	}
}

// startOnceDelayedEchoServer behaves like startEchoServer, except the reply
// to the FIRST request it ever receives is delayed by `delay` before being
// written; every subsequent request on the same connection is answered
// immediately. Each reply echoes back the request's own "i" field so a test
// can tell exactly which request a given reply answers.
func startOnceDelayedEchoServer(t *testing.T, delay time.Duration) (sockPath string) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-ipc-delay-*.sock")
	if err != nil {
		t.Fatalf("tempname: %v", err)
	}
	sockPath = f.Name()
	f.Close()
	os.Remove(sockPath)
	t.Cleanup(func() { os.Remove(sockPath) })

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		first := true
		for {
			body, err := ReadFrame(conn)
			if err != nil {
				return
			}
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			if first {
				first = false
				time.Sleep(delay)
			}
			reply, _ := json.Marshal(map[string]any{"ok": true, "i": m["i"]})
			if err := WriteFrame(conn, reply); err != nil {
				return
			}
		}
	}()

	return sockPath
}

// TestClient_Send_TimeoutThenSend_DrainsStaleReplyNotMisdelivered is the
// explicit desync regression the 2026-08-13 fix requires: a timed-out
// request's reply, arriving LATE on a connection Send now keeps open, must
// never be handed to a later, unrelated Send call as if it were that call's
// own reply.
func TestClient_Send_TimeoutThenSend_DrainsStaleReplyNotMisdelivered(t *testing.T) {
	shrinkSendTimeout(t, 30*time.Millisecond)

	// The first reply is delayed well past SendTimeout (so request #1
	// definitely times out), but short enough that by the time the test
	// issues request #2, the stale reply has already been written by the
	// server and is just sitting in the connection's receive buffer,
	// waiting to be drained.
	sockPath := startOnceDelayedEchoServer(t, 150*time.Millisecond)

	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	if _, err := c.Send(map[string]any{"op": "ping", "i": 1}); !errors.Is(err, ErrTimeout) {
		t.Fatalf("first Send: err = %v, want ErrTimeout", err)
	}

	// Give the server time to actually finish writing the (now stale) first
	// reply before issuing the second request.
	time.Sleep(150 * time.Millisecond)

	resp, err := c.Send(map[string]any{"op": "ping", "i": 2})
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if resp["i"] != float64(2) {
		t.Fatalf("resp[i] = %v, want 2 -- got request #1's STALE reply instead of "+
			"request #2's own reply (framing desync)", resp["i"])
	}
}

// TestClient_Send_AfterTimeout_ReconnectRecoversNormalOperation proves the
// timeout composes with the EXISTING recovery contract (Reconnect /
// engine.go's recoverSidecar, `651f5f8`): after a Send times out and drops
// the connection, Reconnect to a healthy server must restore normal
// operation on the SAME *Client -- exactly the path recoverSidecar already
// takes for a connection that failed for any other reason.
func TestClient_Send_AfterTimeout_ReconnectRecoversNormalOperation(t *testing.T) {
	shrinkSendTimeout(t, 30*time.Millisecond)

	hangSock := startHangingServer(t)
	c, err := Dial(hangSock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	if _, err := c.Send(map[string]any{"op": "ping"}); err == nil {
		t.Fatal("first Send against a wedged sidecar: expected a timeout error, got nil")
	}

	healthySock, closeHealthy := startEchoServer(t, "recovered")
	defer closeHealthy()

	if err := c.Reconnect(healthySock); err != nil {
		t.Fatalf("Reconnect after a timed-out Send: %v", err)
	}

	resp, err := c.Send(map[string]any{"op": "ping"})
	if err != nil {
		t.Fatalf("Send after Reconnect: %v", err)
	}
	if resp["from"] != "recovered" {
		t.Errorf("resp[from] = %v, want recovered -- the SAME *Client must be talking to the new server", resp["from"])
	}
}

func TestClient_Close_IsIdempotent(t *testing.T) {
	sockPath, closeServer := startEchoServer(t, "A")
	defer closeServer()

	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close (must be a no-op, not an error/panic): %v", err)
	}
}

func TestClient_Close_ZeroValue_NoPanic(t *testing.T) {
	c := &Client{}
	if err := c.Close(); err != nil {
		t.Fatalf("Close on a never-connected client: expected nil, got %v", err)
	}
}

// TestClient_ConcurrentSendAndReconnect_NoRace is the -race regression
// coverage for the engine-level fix: engine.go's recoverSidecar reconnects
// a *Client from its OWN background goroutine while OTHER goroutines
// (the MIDI callback, the status ticker) may concurrently call Send() on
// the exact same *Client. Both must serialize through c.mu with no torn
// read/write and no panic -- individual Send() calls during the swap may
// legitimately error (racing which connection they land on), which is
// fine; a race or a panic is not.
func TestClient_ConcurrentSendAndReconnect_NoRace(t *testing.T) {
	sockA, closeA := startEchoServer(t, "A")
	defer closeA()
	sockB, closeB := startEchoServer(t, "B")
	defer closeB()

	c, err := Dial(sockA)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = c.Send(map[string]any{"op": "ping", "i": i})
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			target := sockA
			if i%2 == 1 {
				target = sockB
			}
			_ = c.Reconnect(target)
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
