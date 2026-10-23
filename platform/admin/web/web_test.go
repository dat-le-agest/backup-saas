package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Client-side routes get index.html with 200, even when the router preset 404.
func TestDeepLinksServeIndex(t *testing.T) {
	h, ok := Handler()
	if !ok {
		t.Skip("dashboard not built (npm run build)")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/apps/notes/users", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("deep link: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
}
