package gui

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

func NewAssetHandler(media *MediaRegistry, fallback http.Handler) http.Handler {
	return media.Middleware(fallback)
}
func NewDevAssetHandler(media *MediaRegistry, target *url.URL) http.Handler {
	return NewAssetHandler(media, httputil.NewSingleHostReverseProxy(target))
}
