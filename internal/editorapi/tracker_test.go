package editorapi

import (
	"errors"
	"testing"
	"time"
)

func TestEngineTrackerAppliedFailedAndWait(t *testing.T) {
	tr := NewEngineTracker()
	now := time.UnixMilli(1000)
	tr.Now = func() time.Time { return now }
	if st := tr.EngineState(); st.AppliedSeq != 0 || st.ComposeError != "" || st.AppliedAt != 0 {
		t.Fatalf("fresh = %+v", st)
	}
	tr.Applied(2)
	if st := tr.EngineState(); st.AppliedSeq != 2 || st.AppliedAt != 1000 {
		t.Fatalf("applied = %+v", st)
	}
	// a failure keeps the applied seq and sets the error
	tr.Failed(3, errors.New("boom"))
	st := tr.EngineState()
	if st.AppliedSeq != 2 || st.ComposeError == "" || !contains(st.ComposeError, "boom") {
		t.Fatalf("failed = %+v", st)
	}
	// waiting for the failed seq returns at once, with the error
	t0 := time.Now()
	if st := tr.WaitApplied(3, 2*time.Second); st.ComposeError == "" || time.Since(t0) > time.Second {
		t.Errorf("wait on failed = %+v after %v", st, time.Since(t0))
	}
	// a late success of an OLDER snapshot does not clear a newer failure
	tr.Applied(2)
	if tr.EngineState().ComposeError == "" {
		t.Error("older success cleared the newer failure")
	}
	// a success of the failed or a newer snapshot clears it
	tr.Applied(4)
	if st := tr.EngineState(); st.ComposeError != "" || st.AppliedSeq != 4 {
		t.Errorf("recovered = %+v", st)
	}
	// an older Applied never moves the seq back
	tr.Applied(1)
	if tr.EngineState().AppliedSeq != 4 {
		t.Error("applied seq went back")
	}
	// wait for a seq that arrives later
	done := make(chan EngineState, 1)
	go func() { done <- tr.WaitApplied(5, 3*time.Second) }()
	time.Sleep(30 * time.Millisecond)
	tr.Applied(5)
	select {
	case st := <-done:
		if st.AppliedSeq != 5 {
			t.Errorf("wait result %+v", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not wake")
	}
	// and one that never does times out with the current state
	t0 = time.Now()
	if st := tr.WaitApplied(99, 50*time.Millisecond); st.AppliedSeq != 5 || time.Since(t0) < 40*time.Millisecond {
		t.Errorf("timeout wait = %+v after %v", st, time.Since(t0))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
