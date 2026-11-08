package actions

import "testing"

func TestKeyIsDestructive_TextEditingVariants(t *testing.T) {
	yes := []string{"Ctrl+H", "ctrl + h", "Control+K", "Ctrl+D", "Cmd+X", "Command+x", "Shift+Cmd+X", "Cmd+Backspace", "Cmd+Delete", "Cmd+Option+Escape", "Alt+Cmd+Escape", "Cmd+Q", "Cmd+W", "Delete", "ForwardDelete"}
	no := []string{"Ctrl+C", "Ctrl+A", "Ctrl+E", "Ctrl+X", "X", "H", "K", "Cmd+C", "Cmd+V", "Cmd+Z", "Escape", "Cmd+Escape", "Option+Escape", "Ctrl+Escape", "Cmd+T"}
	for _, k := range yes {
		if !KeyIsDestructive(k) {
			t.Errorf("%q is not classified destructive", k)
		}
	}
	for _, k := range no {
		if KeyIsDestructive(k) {
			t.Errorf("%q is classified destructive", k)
		}
	}
}
