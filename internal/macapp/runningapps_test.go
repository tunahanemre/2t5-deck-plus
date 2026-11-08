package macapp

import "testing"

func TestParseApps(t *testing.T) {
	got := parseApps("com.apple.finder\tFinder\ncom.google.Chrome\tGoogle Chrome\n\nbroken-line\n")
	want := []App{{"com.apple.finder", "Finder"}, {"com.google.Chrome", "Google Chrome"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parseApps = %+v", got)
	}
}

// RunningApps only reads NSWorkspace: no input, no files, no permissions.
func TestRunningAppsHasNoEmptyIDs(t *testing.T) {
	for _, a := range RunningApps() {
		if a.ID == "" {
			t.Fatalf("empty id: %+v", a)
		}
	}
	if AppName("") != "" {
		t.Fatal("AppName(\"\") must be empty")
	}
}
