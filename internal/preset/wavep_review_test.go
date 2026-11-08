package preset

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Wave P final critic pass: race on the trust maps, panic key under a corrupt
// file, unreadable manifest fails closed.

func TestPFinal_BackupsRaceWithBackupNowAndEdits(t *testing.T) {
	root := t.TempDir()
	s, _ := trustStore(t, root)
	defer s.Close()
	mustAdd(t, s, chrome)
	s.MarkFirstRunDone()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					s.Backups()
				}
			}
		}()
	}
	for i := 0; i < 15; i++ {
		mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+T"))
		if _, _, err := s.BackupNowLabelled("race"); err != nil {
			t.Log(err)
		}
		mustAssign(t, s, chrome, "base", "C3", keyA("Cmd+W"))
	}
	if bs := s.Backups(); len(bs) > 0 {
		if _, err := s.RestoreBackupOpts(bs[0].Name, ImportOptions{}); err != nil {
			t.Log(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestPFinal_PanicKeyKeptWhenItsFileIsOverwrittenWithGarbageWhileClosed(t *testing.T) {
	root := t.TempDir()
	s, dir := trustStore(t, root)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C8", map[string]any{"type": "disarm"})
	s.MarkFirstRunDone()
	s.Close()
	if err := os.WriteFile(filepath.Join(dir, chrome+".json"), []byte("{garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := reopenTrust(t, root)
	if d, why := s2.StartDisarmed(func(string) string { return "" }); !d || why != StartReasonExternal {
		t.Fatalf("start = %v %q, want disarmed (external-changes)", d, why)
	}
	p, ok := s2.Snapshot().Preset(chrome)
	if !ok || !hasPanicKey(p) {
		t.Error("the last-good copy with the panic key is not kept in memory")
	}
	if len(s2.PendingExternal()) != 1 {
		t.Errorf("pending = %+v", s2.PendingExternal())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, chrome+".json")); string(got) != "{garbage" {
		t.Error("the file on disk was touched")
	}
}

func TestPFinal_UnreadableOrWrongVersionManifestFailsClosed(t *testing.T) {
	for name, body := range map[string]string{"garbage": "{nope", "version": `{"version":99,"presets":{}}`} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			s, _ := trustStore(t, root)
			mustAdd(t, s, chrome)
			s.MarkFirstRunDone()
			s.Close()
			if err := os.WriteFile(filepath.Join(root, "state", "trust", "manifest.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			s2 := reopenTrust(t, root)
			if d, why := s2.StartDisarmed(func(string) string { return "" }); !d || why != StartReasonExternal {
				t.Fatalf("start = %v %q", d, why)
			}
			found := false
			for _, w := range s2.Warnings() {
				if strings.Contains(w.Message, "manifest.json") {
					found = true
				}
			}
			if !found {
				t.Error("no warning about the unreadable trust record")
			}
		})
	}
	// a missing manifest is a first run, not a failure
	root := t.TempDir()
	s, _ := trustStore(t, root)
	s.MarkFirstRunDone()
	s.Close()
	os.Remove(filepath.Join(root, "state", "trust", "manifest.json"))
	s2 := reopenTrust(t, root)
	if d, why := s2.StartDisarmed(func(string) string { return "" }); d && why == StartReasonExternal {
		t.Error("a missing manifest was treated as tampering")
	}
}
