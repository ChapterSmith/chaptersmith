package gui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAssetFallback(t *testing.T) {
	r, p, _ := mediaFixture(t)
	ref, _ := r.Register(mediaFile(t, p), "audio/mp4")
	calls := 0
	fallback := http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { calls++; w.Write([]byte(q.URL.Path)) })
	server := httptest.NewServer(fallback)
	defer server.Close()
	target, _ := url.Parse(server.URL)
	for _, h := range []http.Handler{NewAssetHandler(r, fallback), NewDevAssetHandler(r, target)} {
		w := request(h, "GET", "/src/main.js", "")
		if w.Body.String() != "/src/main.js" {
			t.Fatal("fallback missing")
		}
		before := calls
		w = request(h, "GET", ref.URL, "bytes=2-5")
		if w.Code != 206 || w.Body.String() != "2345" || calls != before {
			t.Fatal("media reached fallback")
		}
	}
}
