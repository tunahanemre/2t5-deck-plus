package editorapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestClientLogAcceptsAGetAndNeverBreaksOnJunk(t *testing.T) {
	s := &server{}
	for _, m := range []string{"hello", "line\nbreak\x00and nul", string(make([]byte, 5000))} {
		rec := httptest.NewRecorder()
		s.clientLog(rec, httptest.NewRequest(http.MethodGet, "/api/client-log?m="+url.QueryEscape(m), nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status %d", rec.Code)
		}
	}
}
