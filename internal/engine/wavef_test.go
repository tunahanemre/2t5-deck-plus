package engine

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/appdeck"
	"2t5-deck-plus/internal/ipc"
	"2t5-deck-plus/internal/preset"
)

// B3 (critic pass 2): a Resolve-path hold (fineModeHold / modifierHold) pressed
// while Resolve is frontmost and released while the 2T5 DECK+ window has focus
// must still be released; the editor-focus swallow returned before the release.
func TestSelfFrontRelease_ClearsResolveFineModeHold(t *testing.T) {
	fakeSelf(t, tSelf)
	r := newAdRig(t, func(s *preset.Store) {
		nil2(t)(s)
		mustAssign(t, s, preset.ResolveID, "col", "C8", map[string]any{"type": "fineModeHold"})
	})
	r.setFront(preset.ResolveID)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOn(0, 42, 100), 0)
	adWaitFor(t, "fine mode on", func() bool { return r.eng.Status().FineMode })
	r.setFront(tSelf)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOff(0, 42), 0)
	adWaitFor(t, "fine mode released while the editor has focus", func() bool { return !r.eng.Status().FineMode })
}

func TestSelfFrontRelease_ClearsResolveModifierHold(t *testing.T) {
	fakeSelf(t, tSelf)
	r := newAdRig(t, func(s *preset.Store) {
		nil2(t)(s)
		mustAssign(t, s, preset.ResolveID, "col", "C8", map[string]any{"type": "modifierHold", "modifier": "shift"})
	})
	r.setFront(preset.ResolveID)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOn(0, 42, 100), 0)
	adWaitFor(t, "overlay held", func() bool { return r.eng.getHeldOverlay() == "shift" })
	r.setFront(tSelf)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOff(0, 42), 0)
	adWaitFor(t, "overlay released while the editor has focus", func() bool { return r.eng.getHeldOverlay() == "" })
}

// B8: a recovery started for a client that dropSidecar has since closed must not
// relaunch a sidecar or reconnect the closed client.
func TestRecoverSidecar_StaleClientDoesNothing(t *testing.T) {
	origState, origLaunch := resolveStateFn, launchSidecarFn
	resolveStateFn = func() (bool, bool) { return true, true }
	var launches int32
	launchSidecarFn = func(string) (func(), string, error) { atomic.AddInt32(&launches, 1); return func() {}, "", nil }
	t.Cleanup(func() { resolveStateFn, launchSidecarFn = origState, origLaunch })

	e := &Engine{}
	stale := &ipc.Client{}
	// the engine's current client is nil (dropSidecar ran) or another one
	e.recoverSidecar(context.Background(), stale)
	e.sidecarRecoveryWG.Wait()
	if n := atomic.LoadInt32(&launches); n != 0 {
		t.Fatalf("a stale client relaunched the sidecar %d time(s)", n)
	}
	other := &ipc.Client{}
	e.clientPtr.Store(other)
	e.recoverSidecar(context.Background(), stale)
	e.sidecarRecoveryWG.Wait()
	if n := atomic.LoadInt32(&launches); n != 0 {
		t.Fatalf("recovery for a replaced client relaunched the sidecar %d time(s)", n)
	}
}

// ---- B7: nothing is written to Resolve, and no dialog opens, while disarmed ----------------

type opCounter struct {
	mu  sync.Mutex
	ops map[string]int
}

func (o *opCounter) n(op string) int { o.mu.Lock(); defer o.mu.Unlock(); return o.ops[op] }

// startCountingSidecar is a sidecar that answers every frame and counts ops.
func startCountingSidecar(t *testing.T) (string, *opCounter) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-cnt-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	sock := f.Name()
	f.Close()
	os.Remove(sock)
	t.Cleanup(func() { os.Remove(sock) })
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	oc := &opCounter{ops: map[string]int{}}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					body, err := ipc.ReadFrame(c)
					if err != nil {
						return
					}
					var m map[string]any
					_ = json.Unmarshal(body, &m)
					op, _ := m["op"].(string)
					oc.mu.Lock()
					oc.ops[op]++
					oc.mu.Unlock()
					if err := ipc.WriteFrame(c, []byte(`{"ok":true,"page":"color"}`)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return sock, oc
}

func TestDisarmedHoldsBackHighlightRestoreUntilArmed(t *testing.T) {
	h := newLazySidecarHarness(t)
	sock, oc := startCountingSidecar(t)
	h.eng.sockPath = sock
	h.running.Store(true)
	h.front.Store(true)
	ad := appdeck.NewDisarmed()
	t.Cleanup(func() { ad.SetArmed(true) })
	h.eng.SetAppDeck(ad)
	h.start(t)
	waitFor(t, "sidecar connected", func() bool { return h.eng.getClient() != nil })
	time.Sleep(150 * time.Millisecond) // several supervisor polls
	if n := oc.n("restore_highlight"); n != 0 {
		t.Fatalf("restore_highlight reached the sidecar %d time(s) while disarmed", n)
	}
	if !h.eng.highlightRestorePending.Load() {
		t.Error("the held-back restore was not remembered")
	}
	ad.SetArmed(true)
	waitFor(t, "held-back restore runs once armed", func() bool { return oc.n("restore_highlight") == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := oc.n("restore_highlight"); n != 1 {
		t.Errorf("restore_highlight ran %d times", n)
	}
	if h.eng.highlightRestorePending.Load() {
		t.Error("restore still pending after it ran")
	}
}

func TestArmedConnectStillRestoresHighlightAtOnce(t *testing.T) {
	h := newLazySidecarHarness(t)
	sock, oc := startCountingSidecar(t)
	h.eng.sockPath = sock
	h.running.Store(true)
	h.front.Store(true)
	h.eng.SetAppDeck(appdeck.New())
	h.start(t)
	waitFor(t, "restore at connect", func() bool { return oc.n("restore_highlight") == 1 })
}

func TestStopWhileDisarmedSendsNoClearHighlight(t *testing.T) {
	h := newLazySidecarHarness(t)
	sock, oc := startCountingSidecar(t)
	h.eng.sockPath = sock
	h.running.Store(true)
	h.front.Store(true)
	ad := appdeck.New()
	h.eng.SetAppDeck(ad)
	h.start(t)
	waitFor(t, "sidecar connected", func() bool { return h.eng.getClient() != nil })
	ad.SetArmed(false)
	h.eng.Stop()
	if n := oc.n("clear_highlight"); n != 0 {
		t.Errorf("clear_highlight sent %d time(s) on a disarmed Stop", n)
	}
}

func TestQuickExportDialogNotOpenedWhileDisarmed(t *testing.T) {
	e := newTestEngine(t)
	var opened atomic.Int32
	orig := chooseFolderFn
	chooseFolderFn = func() (string, error) { opened.Add(1); return "/tmp/x", nil }
	t.Cleanup(func() { chooseFolderFn = orig })
	ad := appdeck.NewDisarmed()
	t.Cleanup(func() { ad.SetArmed(true) })
	e.SetAppDeck(ad)
	if p, err := e.chooseFolder(); p != "" || err != nil || opened.Load() != 0 {
		t.Fatalf("disarmed: path=%q err=%v dialogs=%d", p, err, opened.Load())
	}
	ad.SetArmed(true)
	if p, _ := e.chooseFolder(); p != "/tmp/x" || opened.Load() != 1 {
		t.Fatalf("armed: path=%q dialogs=%d", p, opened.Load())
	}
}

// ---- H4: hold to fire on the Resolve key path ----------------------------------------------

type holdFake struct {
	mu   sync.Mutex
	fs   []func()
	stop []bool
	ds   []time.Duration
}

func (h *holdFake) after(d time.Duration, f func()) func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := len(h.fs)
	h.fs = append(h.fs, f)
	h.stop = append(h.stop, false)
	h.ds = append(h.ds, d)
	return func() { h.mu.Lock(); h.stop[i] = true; h.mu.Unlock() }
}
func (h *holdFake) n() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.fs) }
func (h *holdFake) fire(i int) {
	h.mu.Lock()
	f := h.fs[i]
	h.mu.Unlock()
	f()
}

func holdRigResolve(t *testing.T, ms any) (*adRig, *holdFake, *[]string) {
	h := &holdFake{}
	origAfter, origSend := holdAfterFn, holdSendKeyFn
	holdAfterFn = h.after
	var sent []string
	var smu sync.Mutex
	holdSendKeyFn = func(s string) error { smu.Lock(); sent = append(sent, s); smu.Unlock(); return nil }
	t.Cleanup(func() { holdAfterFn, holdSendKeyFn = origAfter, origSend })
	r := newAdRig(t, func(s *preset.Store) {
		nil2(t)(s)
		a := map[string]any{"type": "key", "key": "Cmd+Q"}
		if ms != nil {
			a["holdMs"] = ms
		}
		mustAssign(t, s, preset.ResolveID, "col", "C8", a)
	})
	r.setFront(preset.ResolveID)
	time.Sleep(60 * time.Millisecond)
	return r, h, &sent
}

func sentSnap(mu *sync.Mutex, s *[]string) []string {
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), (*s)...)
}

func TestHoldResolveKey_FiresOnlyAfterTheThreshold(t *testing.T) {
	r, h, sent := holdRigResolve(t, 400)
	r.cb(midi.NoteOn(0, 42, 100), 0)
	ev := r.nextEvent(t)
	if ev.Kind != "press" || !strings.Contains(ev.Out.Text, "400") {
		t.Fatalf("press event = %+v", ev)
	}
	if h.n() != 1 || h.ds[0] != 400*time.Millisecond {
		t.Fatalf("timers = %d %v", h.n(), h.ds)
	}
	if len(*sent) != 0 {
		t.Fatalf("key sent at press: %v", *sent)
	}
	h.fire(0)
	adWaitFor(t, "key sent at the threshold", func() bool { return len(*sent) == 1 })
	if (*sent)[0] != "Cmd+Q" {
		t.Errorf("sent %v", *sent)
	}
	ev = r.nextEvent(t)
	if ev.Out.Text != "Cmd+Q" || ev.PresetID != preset.ResolveID {
		t.Errorf("fire event = %+v", ev)
	}
	r.cb(midi.NoteOff(0, 42), 0) // release after the fire: nothing more
	time.Sleep(30 * time.Millisecond)
	if len(*sent) != 1 {
		t.Errorf("sent %v", *sent)
	}
}

func TestHoldResolveKey_ATapNeverFires(t *testing.T) {
	r, h, sent := holdRigResolve(t, 400)
	r.cb(midi.NoteOn(0, 42, 100), 0)
	r.nextEvent(t)
	r.cb(midi.NoteOff(0, 42), 0)
	ev := r.nextEvent(t)
	if ev.Kind != "release" || ev.Out.Text != "hold to fire (400 ms)" {
		t.Fatalf("tap release event = %+v", ev)
	}
	if !h.stop[0] {
		t.Error("the timer was not cancelled")
	}
	h.fire(0) // a timer that raced the cancel
	time.Sleep(30 * time.Millisecond)
	if len(*sent) != 0 {
		t.Errorf("a tap fired: %v", *sent)
	}
}

func TestHoldResolveKey_AbsentIsTheOldImmediatePath(t *testing.T) {
	// byte-identical when holdMs is absent: no timer, the key goes through the unchanged
	// dispatchNoteOn path (keys.Send is a no-op under test; the event reports the control)
	for _, ms := range []any{nil, 0, 0.0} {
		r, h, sent := holdRigResolve(t, ms)
		r.cb(midi.NoteOn(0, 42, 100), 0)
		r.cb(midi.NoteOff(0, 42), 0)
		time.Sleep(120 * time.Millisecond)
		if h.n() != 0 || len(*sent) != 0 {
			t.Errorf("holdMs=%v: timers=%d sent=%v (the hold path must not engage)", ms, h.n(), *sent)
		}
	drain:
		for {
			select {
			case ev := <-r.events:
				if strings.Contains(ev.Out.Text, "hold") {
					t.Errorf("holdMs=%v: event %+v", ms, ev)
				}
			default:
				break drain
			}
		}
	}
}

func TestHoldResolveKey_StopCancelsAndNotResolveFrontSkips(t *testing.T) {
	r, h, sent := holdRigResolve(t, 400)
	r.cb(midi.NoteOn(0, 42, 100), 0)
	r.nextEvent(t)
	r.setFront(tChrome) // the user left Resolve while holding
	time.Sleep(60 * time.Millisecond)
	h.fire(0)
	time.Sleep(30 * time.Millisecond)
	if len(*sent) != 0 {
		t.Errorf("the key went to another app: %v", *sent)
	}
	r.setFront(preset.ResolveID)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOn(0, 42, 100), 0)
	r.nextEvent(t)
	r.eng.Stop()
	if !h.stop[len(h.stop)-1] {
		t.Error("Stop left a hold timer running")
	}
}

// ---- H5: panic disarm from the deck ---------------------------------------------------------

func disarmRig(t *testing.T, setup func(*preset.Store)) *adRig {
	r := newAdRig(t, func(s *preset.Store) { nil2(t)(s); setup(s) })
	t.Cleanup(func() { r.ad.SetArmed(true) })
	return r
}

func TestPanicDisarm_FromAForeignApp(t *testing.T) {
	r := disarmRig(t, func(s *preset.Store) {
		mustAssign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "disarm"})
	})
	if !r.ad.Armed() {
		t.Fatal("rig must start armed")
	}
	var src string
	r.ad.OnArmChange = func(armed bool, s string) { src = s }
	r.cb(midi.NoteOn(0, 42, 100), 0)
	ev := r.nextEvent(t)
	if ev.Kind != "press" || ev.Control != "C8" || !strings.Contains(ev.Out.Text, "disarmed") {
		t.Fatalf("event = %+v", ev)
	}
	if r.ad.Armed() || src != "deck-key C8" {
		t.Errorf("armed=%v source=%q", r.ad.Armed(), src)
	}
	// the next real press is only reported
	r.cb(midi.NoteOn(0, 41, 100), 0)
	ev = r.nextEvent(t)
	if !strings.HasPrefix(ev.Out.Text, "disarmed: ") || len(r.snapKeys()) != 0 {
		t.Errorf("after the panic key: %+v keys=%v", ev, r.snapKeys())
	}
}

func TestPanicDisarm_WhileTheEditorIsFrontmost(t *testing.T) {
	fakeSelf(t, tSelf)
	r := disarmRig(t, func(s *preset.Store) {
		mustAssign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "disarm"})
	})
	r.setFront(tSelf)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOn(0, 41, 100), 0) // an ordinary key: swallowed, as before
	if ev := r.nextEvent(t); !strings.Contains(ev.Out.Text, "editor focused") {
		t.Fatalf("ordinary press = %+v", ev)
	}
	if !r.ad.Armed() {
		t.Fatal("an ordinary press disarmed")
	}
	r.cb(midi.NoteOn(0, 42, 100), 0) // the panic key is NOT swallowed
	ev := r.nextEvent(t)
	if !strings.Contains(ev.Out.Text, "disarmed") || strings.Contains(ev.Out.Text, "editor focused") || r.ad.Armed() {
		t.Errorf("panic key with the editor frontmost: %+v armed=%v", ev, r.ad.Armed())
	}
}

func TestPanicDisarm_ResolveOwnBinding(t *testing.T) {
	r := disarmRig(t, func(s *preset.Store) {
		mustAssign(t, s, preset.ResolveID, "col", "C8", map[string]any{"type": "disarm"})
	})
	r.setFront(preset.ResolveID)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOn(0, 42, 100), 0)
	adWaitFor(t, "disarmed from the Resolve preset", func() bool { return !r.ad.Armed() })
}

func TestPanicDisarm_GlobalFromResolveFront(t *testing.T) {
	r := disarmRig(t, func(s *preset.Store) {
		mustAssign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "disarm"})
	})
	r.setFront(preset.ResolveID)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOn(0, 42, 100), 0) // C8 has no Resolve binding: Global's runs
	adWaitFor(t, "disarmed from Global", func() bool { return !r.ad.Armed() })
}

// ---- wave K, finding 2: a Resolve hold belongs to the arm state it began in -------------------

func TestHoldResolveKey_ArmToggleBetweenPressAndFireCancels(t *testing.T) {
	r, h, sent := holdRigResolve(t, 400)
	t.Cleanup(func() { r.ad.SetArmed(true) })
	r.cb(midi.NoteOn(0, 42, 100), 0)
	r.nextEvent(t)
	r.ad.SetArmed(false)
	r.ad.SetArmed(true) // a quick toggle
	h.fire(0)
	time.Sleep(30 * time.Millisecond)
	if len(*sent) != 0 {
		t.Errorf("a hold from before the toggle fired: %v", *sent)
	}
	// a fresh press after the toggle works
	r.cb(midi.NoteOff(0, 42), 0)
	time.Sleep(30 * time.Millisecond)
	r.cb(midi.NoteOn(0, 42, 100), 0)
	r.nextEvent(t)
	h.fire(h.n() - 1)
	adWaitFor(t, "fresh hold fires", func() bool { return len(*sent) == 1 })
}

// ---- wave K, finding 5: the panic key wins over every cascade ---------------------------------

func TestPanicKey_BeatsAnAppBindingOnTheSameControl(t *testing.T) {
	r := disarmRig(t, func(s *preset.Store) {
		mustAssign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "disarm"})
		mustAssign(t, s, tChrome, "base", "C8", map[string]any{"type": "key", "key": "Cmd+T"}) // shadows Global in Chrome
	})
	r.cb(midi.NoteOn(0, 42, 100), 0)
	ev := r.nextEvent(t)
	if r.ad.Armed() || !strings.Contains(ev.Out.Text, "disarmed") || ev.Action != "generic.disarm" || len(r.snapKeys()) != 0 {
		t.Errorf("armed=%v event=%+v keys=%v", r.ad.Armed(), ev, r.snapKeys())
	}
}

func TestPanicKey_BeatsAResolveBindingOnTheSameControl(t *testing.T) {
	r := disarmRig(t, func(s *preset.Store) {
		mustAssign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "disarm"})
		mustAssign(t, s, preset.ResolveID, "col", "C8", map[string]any{"type": "key", "key": "Cmd+Q"})
	})
	r.setFront(preset.ResolveID)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOn(0, 42, 100), 0)
	adWaitFor(t, "disarmed although Resolve binds C8", func() bool { return !r.ad.Armed() })
}

func TestPanicKey_AnAppsOwnDisarmWorksEverywhereToo(t *testing.T) {
	r := disarmRig(t, func(s *preset.Store) {
		mustAssign(t, s, tChrome, "base", "C8", map[string]any{"type": "disarm"})
	})
	r.setFront(preset.ResolveID)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOn(0, 42, 100), 0)
	adWaitFor(t, "disarmed", func() bool { return !r.ad.Armed() })
}

func TestPanicKey_RunsBeforeAnyFrontmostLookupOrSidecarWork(t *testing.T) {
	r := disarmRig(t, func(s *preset.Store) {
		mustAssign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "disarm"})
	})
	time.Sleep(80 * time.Millisecond) // let the frontmost cache expire
	var lookups atomic.Int32
	orig := frontmostBundleIDFn
	frontmostBundleIDFn = func() string { lookups.Add(1); return orig() }
	t.Cleanup(func() { frontmostBundleIDFn = orig })
	r.cb(midi.NoteOn(0, 42, 100), 0)
	adWaitFor(t, "disarmed", func() bool { return !r.ad.Armed() })
	if n := lookups.Load(); n != 0 {
		t.Errorf("%d frontmost lookups before the panic press was handled (the callback must not wait on anything slow first)", n)
	}
}

func TestPanicKey_NoPanicAssignmentChangesNothing(t *testing.T) {
	r := disarmRig(t, func(s *preset.Store) {})
	r.cb(midi.NoteOn(0, 41, 100), 0)
	ev := r.nextEvent(t)
	if !r.ad.Armed() || ev.Out.Text != "Cmd+T" {
		t.Errorf("armed=%v event=%+v", r.ad.Armed(), ev)
	}
}

func TestHoldResolveKey_HotReloadCancelsPendingHolds(t *testing.T) {
	r, h, sent := holdRigResolve(t, 400)
	r.cb(midi.NoteOn(0, 42, 100), 0)
	r.nextEvent(t)
	r.recompose(t) // SetConfig: a hot reload while the control is held
	if !h.stop[0] {
		t.Error("the hold timer survived the reload")
	}
	h.fire(0)
	time.Sleep(30 * time.Millisecond)
	if len(*sent) != 0 {
		t.Errorf("a hold fired across a hot reload: %v", *sent)
	}
}
