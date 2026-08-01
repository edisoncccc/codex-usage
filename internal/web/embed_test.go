package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestHandlerUsesContentVersionedAssets(t *testing.T) {
	server := httptest.NewServer(Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("index cache control=%q", got)
	}
	text := string(body)
	for _, pattern := range []string{`/styles\.css\?v=[0-9a-f]{12}`, `/i18n\.js\?v=[0-9a-f]{12}`, `/app\.js\?v=[0-9a-f]{12}`} {
		if !regexp.MustCompile(pattern).MatchString(text) {
			t.Fatalf("index missing versioned asset %q", pattern)
		}
	}

	for _, asset := range []string{"/styles.css?v=test", "/i18n.js?v=test", "/app.js?v=test"} {
		assetResponse, err := http.Get(server.URL + asset)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, assetResponse.Body)
		assetResponse.Body.Close()
		if assetResponse.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d", asset, assetResponse.StatusCode)
		}
		if got := assetResponse.Header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
			t.Fatalf("%s cache control=%q", asset, got)
		}
	}
}
