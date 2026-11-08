package aiaccess

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type rec struct {
	mu   sync.Mutex
	reqs []string
	hdr  http.Header
}

func (r *rec) ServeHTTP(w http.ResponseWriter, q *http.Request) {
	b, _ := io.ReadAll(q.Body)
	r.mu.Lock()
	r.reqs = append(r.reqs, q.Method+" "+q.URL.RequestURI()+" "+string(b))
	r.hdr = q.Header.Clone()
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(q.URL.Path, "/nope/") {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"unknown action","field":"assignment"}`))
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func setup(t *testing.T) (*Service, *rec, string) {
	t.Helper()
	dir := t.TempDir()
	r := &rec{}
	var audits []string
	s := New(Options{Dir: dir, Editor: r, EditorToken: "ed-tok", Exe: "/x/ld deck", Port: 1,
		Audit: func(k, text, src string, d map[string]any) { audits = append(audits, k+":"+text) }})
	t.Cleanup(s.Close)
	return s, r, dir
}

func TestOffByDefaultAndOnOff(t *testing.T) {
	s, _, dir := setup(t)
	if st := s.AIStatus(); st.Enabled || st.Address != "" {
		t.Fatalf("must start off: %+v", st)
	}
	if _, err := (&Client{Dir: dir}).Call("status", nil); err != ErrOff {
		t.Fatalf("off client: %v", err)
	}
	st, err := s.SetAIAccess(true)
	if err != nil || !st.Enabled {
		t.Fatalf("on: %v %+v", err, st)
	}
	if !strings.HasPrefix(st.Address, "127.0.0.1:") {
		t.Fatalf("not loopback: %s", st.Address)
	}
	fi, err := os.Stat(filepath.Join(dir, StateFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode: %v %v", err, fi)
	}
	if !strings.Contains(st.SetupLine, "claude mcp add 2t5-deck-plus -- '/x/ld deck' mcp") {
		t.Fatalf("setup line: %s", st.SetupLine)
	}
	c := &Client{Dir: dir}
	if _, err := c.Call("status", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAIAccess(false); err != nil {
		t.Fatal(err)
	}
	if _, err := net.Dial("tcp", st.Address); err == nil {
		t.Fatal("port still open after off")
	}
	if _, err := c.Call("status", nil); err != ErrOff {
		t.Fatalf("after off: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, StateFile))
	if strings.Contains(string(b), "token\":\"") {
		t.Fatalf("token left in the file after off: %s", b)
	}
}

func TestGuardsAndAllowlist(t *testing.T) {
	s, r, dir := setup(t)
	st, _ := s.SetAIAccess(true)
	state, _ := ReadState(dir)
	do := func(method, path, tok, host, origin, body string) int {
		req, _ := http.NewRequest(method, "http://"+st.Address+path, strings.NewReader(body))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if host != "" {
			req.Host = host
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := do("GET", "/ai/v1/ping", "", "", "", ""); c != 401 {
		t.Fatalf("no token: %d", c)
	}
	if c := do("GET", "/ai/v1/ping", "wrong", "", "", ""); c != 401 {
		t.Fatalf("wrong token: %d", c)
	}
	if c := do("GET", "/ai/v1/ping", state.Token, "evil.example", "", ""); c != 403 {
		t.Fatalf("rebinding host: %d", c)
	}
	if c := do("GET", "/ai/v1/ping", state.Token, "", "http://evil.example", ""); c != 403 {
		t.Fatalf("origin: %d", c)
	}
	if c := do("GET", "/ai/v1/ping", state.Token, "", "", ""); c != 200 {
		t.Fatalf("good: %d", c)
	}
	// nothing outside the tool table is reachable: no arm, no simulate, no raw /api
	for _, p := range []string{"/api/arm", "/api/simulate", "/api/deck-state", "/api/ai-access", "/ai/v1/call/arm", "/ai/v1/call/press"} {
		if c := do("POST", p, state.Token, "", "", `{}`); c != 404 {
			t.Fatalf("%s reachable: %d", p, c)
		}
	}
	if len(r.reqs) != 0 {
		t.Fatalf("editor was reached: %v", r.reqs)
	}
	for _, tl := range Tools() {
		if strings.Contains(tl.Name, "arm") || strings.Contains(tl.Name, "press") || strings.Contains(tl.Name, "mouse") || strings.Contains(tl.Name, "key") {
			t.Fatalf("forbidden tool %s", tl.Name)
		}
	}
}

func TestWritesUseTheEditorPath(t *testing.T) {
	s, r, dir := setup(t)
	s.SetAIAccess(true)
	c := &Client{Dir: dir}
	if _, err := c.Call("set_assignment", map[string]any{"app": "com.apple.finder", "bank": "base", "control": "Knob1", "assignment": map[string]any{"action": "x", "n": json.Number("3")}}); err != nil {
		t.Fatal(err)
	}
	if len(r.reqs) != 1 || !strings.HasPrefix(r.reqs[0], "PUT /api/preset/com.apple.finder/assign ") || !strings.Contains(r.reqs[0], `"control":"Knob1"`) {
		t.Fatalf("%v", r.reqs)
	}
	if r.hdr.Get("X-LD-Editor") != "1" || r.hdr.Get("X-LD-Token") != "ed-tok" {
		t.Fatalf("editor headers: %v", r.hdr)
	}
	if _, err := c.Call("clear_assignment", map[string]any{"app": "a", "bank": "b", "control": "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call("undo", nil); err != nil {
		t.Fatal(err)
	}
	if got := r.reqs[len(r.reqs)-1]; !strings.HasPrefix(got, "POST /api/undo") {
		t.Fatal(got)
	}
	if st := s.AIStatus(); st.Changes != 3 {
		t.Fatalf("changes %d", st.Changes)
	}
	// the editor's refusal comes back with its reason
	_, err := c.Call("set_assignment", map[string]any{"app": "nope", "bank": "b", "control": "c", "assignment": map[string]any{}})
	ae, ok := err.(*APIError)
	if !ok || ae.Status != 400 || ae.Field != "assignment" {
		t.Fatalf("refusal: %v", err)
	}
	// argument errors never reach the editor
	n := len(r.reqs)
	if _, err := c.Call("set_value", map[string]any{"app": "a"}); err == nil || len(r.reqs) != n {
		t.Fatalf("missing args: %v", err)
	}
}

func TestMCPHandshake(t *testing.T) {
	s, _, dir := setup(t)
	s.SetAIAccess(true)
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_apps","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"arm","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := ServeMCP(&Client{Dir: dir}, strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 { // the notification gets no answer
		t.Fatalf("%d answers:\n%s", len(lines), out.String())
	}
	var init, list, call, bad, nometh map[string]any
	for i, p := range []*map[string]any{&init, &list, &call, &bad, &nometh} {
		json.Unmarshal([]byte(lines[i]), p)
	}
	if init["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatal(lines[0])
	}
	if n := len(list["result"].(map[string]any)["tools"].([]any)); n != len(Tools()) {
		t.Fatal(n)
	}
	if call["result"].(map[string]any)["isError"] == true {
		t.Fatal(lines[2])
	}
	if bad["error"] == nil || nometh["error"] == nil {
		t.Fatal("unknown tool / method must be errors")
	}
}

func TestCLI(t *testing.T) {
	var out, errb bytes.Buffer
	t.Setenv(EnvDir, t.TempDir())
	if code := RunCLI([]string{"apps"}, nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "AI access is off") {
		t.Fatalf("off: %d %s", code, errb.String())
	}
	out.Reset()
	if code := RunCLI([]string{"tools"}, nil, &out, &errb); code != 0 || !strings.Contains(out.String(), "set_assignment") {
		t.Fatalf("tools: %d %s", code, out.String())
	}
	if _, a, err := cliArgs([]string{"value", "app", "base", "Knob1", "sens", "1.5"}); err != nil || a["value"].(json.Number).String() != "1.5" {
		t.Fatalf("%v %v", a, err)
	}
}
