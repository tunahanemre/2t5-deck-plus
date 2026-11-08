package editorapi

import (
	"sync"
	"time"
)

// EngineTracker is the EngineProvider the app feeds from its compose loop:
// Applied after a snapshot was composed and handed to the engine, Failed when
// that did not work (the engine then keeps running the previous config).
type EngineTracker struct {
	// Now stamps AppliedAt; nil = time.Now.
	Now func() time.Time

	mu        sync.Mutex
	st        EngineState
	failedSeq int
	changed   chan struct{} // closed and replaced on every change
}

// NewEngineTracker starts with nothing applied.
func NewEngineTracker() *EngineTracker { return &EngineTracker{changed: make(chan struct{})} }

func (t *EngineTracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *EngineTracker) bump() {
	close(t.changed)
	t.changed = make(chan struct{})
}

// Applied records that snapshot seq is now what the engine runs. It clears a
// compose error unless a newer snapshot already failed.
func (t *EngineTracker) Applied(seq int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if seq < t.st.AppliedSeq {
		return
	}
	t.st.AppliedSeq, t.st.AppliedAt = seq, t.now().UnixMilli()
	if t.failedSeq <= seq {
		t.st.ComposeError, t.failedSeq = "", 0
	}
	t.bump()
}

// Failed records that composing snapshot seq failed.
func (t *EngineTracker) Failed(seq int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if seq < t.failedSeq {
		return
	}
	t.failedSeq = seq
	t.st.ComposeError = "the engine could not take this change: " + err.Error()
	t.bump()
}

// EngineState implements EngineProvider.
func (t *EngineTracker) EngineState() EngineState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st
}

// WaitApplied implements EngineProvider: it returns once the engine runs
// snapshot seq (or newer), once a compose of seq (or newer) failed, or after d.
func (t *EngineTracker) WaitApplied(seq int, d time.Duration) EngineState {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		t.mu.Lock()
		st, failed, ch := t.st, t.failedSeq, t.changed
		t.mu.Unlock()
		if st.AppliedSeq >= seq || (failed != 0 && failed >= seq) {
			return st
		}
		select {
		case <-ch:
		case <-deadline.C:
			return st
		}
	}
}
