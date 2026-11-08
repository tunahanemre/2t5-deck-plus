package editorapi

import (
	"sync"
	"time"
)

// ArmMarkerWriter keeps the presets folder's .disarmed marker in step with the
// arm switch without ever making the caller wait for the disk. Set(armed) is
// called from wherever the switch is asked to change (including the MIDI
// callback, right after a panic disarm): it only records the wanted state and
// wakes a single writer goroutine, which applies the LATEST wanted state (Mark
// when disarmed, Clear when armed). Rapid toggles collapse to the last one, so
// the marker can lag the switch by a moment but never ends up saying the
// opposite.
//
// A failed write is retried with backoff for as long as that state is still the
// wanted one, and LastError() says what is wrong so the editor can show "could
// not save the disarmed state" (warnings, preflight). Close applies the pending
// state once more and stops.
type ArmMarkerWriter struct {
	Mark  func() error // write the marker (the deck is disarmed)
	Clear func() error // remove it (the deck is armed)
	Logf  func(string, ...any)
	// Backoff is the wait before retry n (1, 2, ...); nil = 1 s doubling to 30 s.
	Backoff func(attempt int) time.Duration
	// CloseTimeout bounds Close when the disk is wedged (a hung write): 0 = 3 s.
	CloseTimeout time.Duration

	mu      sync.Mutex
	want    *bool
	lastErr string
	sig     chan struct{}
	done    chan struct{}
	started bool
	closed  bool
}

func defaultBackoff(n int) time.Duration {
	d := time.Second << uint(n-1)
	if d > 30*time.Second || d <= 0 {
		d = 30 * time.Second
	}
	return d
}

// start must be called with mu held.
func (w *ArmMarkerWriter) start() {
	if w.started {
		return
	}
	w.started = true
	w.sig = make(chan struct{}, 1)
	w.done = make(chan struct{})
	go w.run(w.sig, w.done)
}

func (w *ArmMarkerWriter) apply(v bool) error {
	if v {
		return w.Clear()
	}
	return w.Mark()
}

func (w *ArmMarkerWriter) run(sig chan struct{}, done chan struct{}) {
	defer close(done)
	backoff := w.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	attempt := 0
	var retry <-chan time.Time
	for {
		closing := false
		select {
		case _, ok := <-sig:
			closing = !ok
			attempt = 0
		case <-retry:
		}
		retry = nil
		w.mu.Lock()
		var v *bool
		if w.want != nil {
			c := *w.want
			v = &c
		}
		w.mu.Unlock()
		if v != nil {
			err := w.apply(*v)
			w.mu.Lock()
			if err == nil {
				if w.want != nil && *w.want == *v {
					w.want = nil
				}
				w.lastErr = ""
			} else {
				w.lastErr = err.Error()
			}
			w.mu.Unlock()
			if err != nil {
				if w.Logf != nil {
					w.Logf("[editor] could not update the disarmed marker (attempt %d): %v", attempt+1, err)
				}
				if !closing {
					attempt++
					retry = time.After(backoff(attempt))
				}
			}
		}
		if closing {
			return
		}
	}
}

// Set records the wanted state; it never blocks on the disk and is safe to call
// concurrently with Close (the wake-up is sent while the lock is held, so Close
// can never close the channel under it).
func (w *ArmMarkerWriter) Set(armed bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.start()
	w.want = &armed
	select {
	case w.sig <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// LastError is the text of the newest failed marker write that has not been
// followed by a successful one ("" when the marker is in step).
func (w *ArmMarkerWriter) LastError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastErr
}

// Close applies any pending state once and stops the writer.
func (w *ArmMarkerWriter) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	started := w.started
	if started {
		close(w.sig)
	}
	done := w.done
	w.mu.Unlock()
	if !started {
		return
	}
	limit := w.CloseTimeout
	if limit <= 0 {
		limit = 3 * time.Second
	}
	select {
	case <-done:
	case <-time.After(limit):
		// a wedged disk must not hang the app's quit: the marker stays as it was
		if w.Logf != nil {
			w.Logf("[editor] gave up writing the disarmed marker after %v (the disk does not answer)", limit)
		}
	}
}
