package editorapi

import "testing"

// GET /api/about feeds the "About 2T5 DECK+" view; POST /api/open-link opens one of its three
// links in the default browser. The caller names an id, never a URL.

func TestAbout_NamesTheAppVersionAndTheThreeLinks(t *testing.T) {
	e := envWith(t, false, func(d *Deps) { d.Version = "1.2.3" })
	code, out := e.do(t, "GET", "/api/about", "")
	if code != 200 || out["version"] != "1.2.3" || out["name"] != "2T5 DECK+" || out["by"] != "2T5 Film" {
		t.Fatalf("about = %d %v", code, out)
	}
	links, _ := out["links"].([]any)
	got := map[string]string{}
	for _, l := range links {
		m := l.(map[string]any)
		got[m["id"].(string)] = m["url"].(string)
	}
	want := map[string]string{"site": "https://2t5.co", "instagram": "https://instagram.com/tunahanemre", "sponsors": "https://github.com/sponsors/tunahanemre"}
	if len(got) != 3 {
		t.Fatalf("links = %v", got)
	}
	for id, u := range want {
		if got[id] != u {
			t.Errorf("link %s = %q, want %q", id, got[id], u)
		}
	}
}

func TestAbout_VersionDefaultsToUnknownNotEmpty(t *testing.T) {
	e := newEnv(t, false)
	if code, out := e.do(t, "GET", "/api/about", ""); code != 200 || out["version"] != "" {
		t.Errorf("no version set: %d %v", code, out)
	}
}

func TestOpenLink_OnlyTheThreeIdsAndNeverAnArbitraryURL(t *testing.T) {
	rec := &recURL{}
	e := envWith(t, false, func(d *Deps) { d.OpenURL = rec.open })
	for id, url := range map[string]string{"site": "https://2t5.co", "instagram": "https://instagram.com/tunahanemre", "sponsors": "https://github.com/sponsors/tunahanemre"} {
		code, out := e.do(t, "POST", "/api/open-link", `{"id":"`+id+`"}`)
		if code != 200 || out["ok"] != true {
			t.Fatalf("%s: %d %v", id, code, out)
		}
		if got := rec.urls[len(rec.urls)-1]; got != url {
			t.Errorf("%s opened %q, want %q", id, got, url)
		}
	}
	n := len(rec.urls)
	for _, bad := range []string{`{"id":"https://example.com"}`, `{"id":"file:///etc/passwd"}`, `{"id":""}`, `{"url":"https://2t5.co"}`, `{}`, `not json`} {
		if code, _ := e.do(t, "POST", "/api/open-link", bad); code != 400 {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
	if len(rec.urls) != n {
		t.Errorf("a refused request opened something: %v", rec.urls[n:])
	}
}

func TestOpenLink_NoOpenerIs404(t *testing.T) {
	e := newEnv(t, false)
	if code, _ := e.do(t, "POST", "/api/open-link", `{"id":"site"}`); code != 404 {
		t.Errorf("without an opener = %d, want 404", code)
	}
}

func TestOpenLink_NeedsTheTokenLikeEveryMutation(t *testing.T) {
	rec := &recURL{}
	e := envWith(t, false, func(d *Deps) { d.OpenURL = rec.open; d.Token = testTok })
	if r := rawReq(e, "POST", "/api/open-link", `{"id":"site"}`, nil, "127.0.0.1:2541"); r.Code != 403 || len(rec.urls) != 0 {
		t.Errorf("no token: %d, opened %v", r.Code, rec.urls)
	}
	if r := rawReq(e, "POST", "/api/open-link", `{"id":"site"}`, map[string]string{"X-LD-Token": testTok}, "127.0.0.1:2541"); r.Code != 200 || len(rec.urls) != 1 {
		t.Errorf("with token: %d, opened %v", r.Code, rec.urls)
	}
}
