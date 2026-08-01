package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed static/*
var assets embed.FS

func Handler() http.Handler {
	sub, _ := fs.Sub(assets, "static")
	files := http.FileServer(http.FS(sub))
	index, _ := fs.ReadFile(sub, "index.html")
	styles, _ := fs.ReadFile(sub, "styles.css")
	i18n, _ := fs.ReadFile(sub, "i18n.js")
	script, _ := fs.ReadFile(sub, "app.js")
	index = bytes.ReplaceAll(index, []byte(`href="/styles.css"`),
		[]byte(`href="/styles.css?v=`+assetVersion(styles)+`"`))
	index = bytes.ReplaceAll(index, []byte(`src="/app.js"`),
		[]byte(`src="/app.js?v=`+assetVersion(script)+`"`))
	index = bytes.ReplaceAll(index, []byte(`src="/i18n.js"`),
		[]byte(`src="/i18n.js?v=`+assetVersion(i18n)+`"`))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			path = "index.html"
		}
		if path == "index.html" {
			w.Header().Set("Cache-Control", "no-store")
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
			return
		} else {
			// The index points at content-derived URLs, so a binary upgrade always
			// receives matching HTML/CSS/JS even when Edge retains an older asset.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + path
		files.ServeHTTP(w, r2)
	})
}

func assetVersion(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:6])
}
