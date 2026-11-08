package editorapi

import (
	"encoding/json"
	"net/http"
)

// GET /api/about answers the "About 2T5 DECK+" view: product, maker, version and its three links.
// POST /api/open-link opens one of those links in the default browser. The packaged window
// (Wails) cannot follow a target=_blank link, so the editor asks the app to open it, the same way
// POST /api/open-settings opens a System Settings pane. The caller names an id, never a URL.

type aboutLink struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

var aboutLinks = []aboutLink{
	{"site", "2t5.co", "https://2t5.co"},
	{"instagram", "instagram.com/tunahanemre", "https://instagram.com/tunahanemre"},
	{"sponsors", "Donate", "https://github.com/sponsors/tunahanemre"},
}

func (s *server) routesAbout(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/about", s.about)
	mux.HandleFunc("POST /api/open-link", s.openLink)
}

func (s *server) about(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"name": "2T5 DECK+", "by": "2T5 Film", "version": s.d.Version, "links": aboutLinks, "canOpen": s.d.OpenURL != nil})
}

func (s *server) openLink(w http.ResponseWriter, r *http.Request) {
	if s.d.OpenURL == nil {
		writeErr(w, http.StatusNotFound, "", "this host cannot open links")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "id", "expected {\"id\": \"site\"}")
		return
	}
	for _, l := range aboutLinks {
		if l.ID == body.ID {
			if err := s.d.OpenURL(l.URL); err != nil {
				writeErr(w, http.StatusInternalServerError, "", "could not open the link: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": l.ID})
			return
		}
	}
	writeErr(w, http.StatusBadRequest, "id", "unknown link")
}
