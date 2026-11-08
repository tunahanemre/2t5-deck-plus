package ipc

import "testing"

// Wave E, F7: a gate refuses requests before anything is written.
func TestClient_Gate_RefusesWithoutSendingAndLiftsAgain(t *testing.T) {
	sock, closeSrv := startEchoServer(t, "gate")
	defer closeSrv()
	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	blocked := map[string]bool{"jog": true}
	c.SetGate(func(op string) bool { return blocked[op] })

	resp, err := c.Send(map[string]any{"op": "jog", "frames": 1})
	if err != nil {
		t.Fatalf("gated Send returned an error: %v", err)
	}
	if resp["ok"] != false || resp["disarmed"] != true || resp["from"] != nil {
		t.Errorf("gated reply = %v (it must not reach the server)", resp)
	}
	resp, err = c.Send(map[string]any{"op": "status"})
	if err != nil || resp["from"] != "gate" {
		t.Errorf("allowed op = %v %v", resp, err)
	}
	// a non-map request is never gated (no op to read)
	if resp, err = c.Send([]string{"x"}); err != nil || resp["from"] != "gate" {
		t.Errorf("non-map = %v %v", resp, err)
	}
	c.SetGate(nil)
	if resp, err = c.Send(map[string]any{"op": "jog"}); err != nil || resp["from"] != "gate" {
		t.Errorf("after lifting the gate = %v %v", resp, err)
	}
}
