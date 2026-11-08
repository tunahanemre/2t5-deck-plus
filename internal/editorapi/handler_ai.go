package editorapi

import (
	"net/http"
)

// AIAccessStatus is the editor's view of the "AI access" switch (GET /api/ai-access).
// It never carries the token: the AI tool's own client reads that from the 0600 file.
type AIAccessStatus struct {
	Enabled bool `json:"enabled"`
	// Address is the loopback address the AI listener is on ("" when off).
	Address string `json:"address,omitempty"`
	// TokenFile is where the per-install token lives (shown so the user can see what guards the door).
	TokenFile string `json:"tokenFile,omitempty"`
	// SetupLine is the one command to paste into an AI tool that speaks MCP (Claude Code).
	SetupLine string `json:"setupLine,omitempty"`
	// MCPConfig is the same server as a JSON block for tools configured by file (Cursor, Gemini CLI, Codex ...).
	MCPConfig string `json:"mcpConfig,omitempty"`
	// Command is the terminal command's path (also what the MCP setup runs).
	Command string `json:"command,omitempty"`
	// Tools is how many tools the AI door offers; ReadOnly how many of them cannot change anything.
	Tools    int `json:"tools"`
	ReadOnly int `json:"readOnly"`
	// Changes counts the edits an AI tool made since the switch went on.
	Changes int `json:"changes"`
}

// AIAccessProvider is the AI-access switch the app (or the dev server) owns. nil in Deps = 404 and the editor hides the control.
type AIAccessProvider interface {
	AIStatus() AIAccessStatus
	// SetAIAccess switches the listener on or off and returns the new status.
	SetAIAccess(on bool) (AIAccessStatus, error)
}

func (s *server) routesAI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ai-access", s.aiGet)
	mux.HandleFunc("PUT /api/ai-access", s.aiPut)
}

func (s *server) aiGet(w http.ResponseWriter, r *http.Request) {
	if s.d.AI == nil {
		writeErr(w, http.StatusNotFound, "", "AI access is not available in this host")
		return
	}
	writeJSON(w, 200, s.d.AI.AIStatus())
}

func (s *server) aiPut(w http.ResponseWriter, r *http.Request) {
	if s.d.AI == nil {
		writeErr(w, http.StatusNotFound, "", "AI access is not available in this host")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "enabled", "enabled (true or false) is required")
		return
	}
	st, err := s.d.AI.SetAIAccess(*body.Enabled)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", "AI access could not be switched "+map[bool]string{true: "on", false: "off"}[*body.Enabled]+": "+err.Error())
		return
	}
	if s.d.Events != nil {
		s.d.Events.Add("ai", map[bool]string{true: "AI access switched on", false: "AI access switched off"}[st.Enabled], "editor", nil)
	}
	writeJSON(w, 200, st)
}
