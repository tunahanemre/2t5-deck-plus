package main

import "testing"

func devDeps(t *testing.T, s *Server) (map[string]map[string]any, map[string]any) {
	t.Helper()
	rec := get(t, s.Handler, "GET", "/api/dependencies", "")
	if rec.Code != 200 {
		t.Fatalf("GET /api/dependencies = %d %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec)
	by := map[string]map[string]any{}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		by[m["id"].(string)] = m
	}
	return by, out
}

func TestDevSystem_StartsWithEverythingFine(t *testing.T) {
	s, _ := testServer(t, true)
	by, out := devDeps(t, s)
	for _, id := range []string{"macos-version", "macos-screen-reads", "loupedeck", "no-other-deck-app", "accessibility", "screen-recording", "python3", "resolve-install"} {
		if by[id]["status"] != "ok" {
			t.Errorf("%s = %v (%v)", id, by[id]["status"], by[id]["detail"])
		}
	}
	if out["ok"] != true {
		t.Errorf("ok = %v", out["ok"])
	}
}

func TestDevSystem_FakesPythonResolveDeviceOtherAppAndOS(t *testing.T) {
	s, _ := testServer(t, true)
	post := func(body string) {
		t.Helper()
		if rec := get(t, s.Handler, "POST", "/api/dev/system", body); rec.Code != 200 {
			t.Fatalf("POST /api/dev/system %s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	post(`{"python":"missing","device":"absent","otherApp":"Loupedeck","os":"10.15.7","resolveInstalled":false}`)
	by, out := devDeps(t, s)
	for id, want := range map[string]string{"python3": "missing", "loupedeck": "missing", "no-other-deck-app": "missing", "macos-version": "missing", "resolve-install": "missing"} {
		if by[id]["status"] != want {
			t.Errorf("%s = %v, want %s", id, by[id]["status"], want)
		}
	}
	req, _ := out["requiredMissing"].([]any)
	if len(req) < 3 {
		t.Errorf("requiredMissing = %v", req)
	}
	// scripting: Resolve running but the helper cannot connect
	post(`{"python":"ok","device":"present","otherApp":"","os":"15.3.1","resolveInstalled":true,"resolveRunning":true,"scripting":"retrying"}`)
	by, _ = devDeps(t, s)
	if by["resolve-scripting"]["status"] != "missing" {
		t.Errorf("resolve-scripting = %v", by["resolve-scripting"]["status"])
	}
	post(`{"scripting":"connected"}`)
	by, _ = devDeps(t, s)
	if by["resolve-scripting"]["status"] != "ok" {
		t.Errorf("resolve-scripting = %v", by["resolve-scripting"]["status"])
	}
	// errors read as unknown, never ok
	post(`{"python":"error","device":"error"}`)
	by, _ = devDeps(t, s)
	if by["python3"]["status"] != "unknown" || by["loupedeck"]["status"] != "unknown" {
		t.Errorf("python=%v loupedeck=%v", by["python3"]["status"], by["loupedeck"]["status"])
	}
	// bad input is refused
	for _, bad := range []string{`{"python":"maybe"}`, `{"device":"x"}`, `{"os":"banana"}`, `{"scripting":"sometimes"}`, `nope`} {
		if rec := get(t, s.Handler, "POST", "/api/dev/system", bad); rec.Code != 400 {
			t.Errorf("%s = %d, want 400", bad, rec.Code)
		}
	}
}

func TestDevSystem_PermissionsFeedDependencies(t *testing.T) {
	s, _ := testServer(t, true)
	get(t, s.Handler, "POST", "/api/dev/permissions", `{"accessibility":false}`)
	by, out := devDeps(t, s)
	if by["accessibility"]["status"] != "missing" {
		t.Errorf("accessibility = %v", by["accessibility"]["status"])
	}
	if req, _ := out["requiredMissing"].([]any); len(req) != 1 || req[0] != "accessibility" {
		t.Errorf("requiredMissing = %v", req)
	}
}

func TestDevSystem_OpenSettingsIsRecordedNeverOpened(t *testing.T) {
	s, _ := testServer(t, true)
	if rec := get(t, s.Handler, "POST", "/api/open-settings", `{"pane":"accessibility"}`); rec.Code != 200 {
		t.Fatalf("open-settings = %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(t, s.Handler, "POST", "/api/open-settings", `{"pane":"https://example.com"}`); rec.Code != 400 {
		t.Errorf("a URL is not a pane: %d", rec.Code)
	}
	out := decodeBody(t, get(t, s.Handler, "GET", "/api/dev/opened", ""))
	list, _ := out["opened"].([]any)
	if len(list) != 1 || list[0] != "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility" {
		t.Errorf("opened = %v", out)
	}
}
