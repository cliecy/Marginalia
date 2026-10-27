package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFSIsTheDistFolder(t *testing.T) {
	fsys := FS()
	// The committed placeholder is always there; index.html only after a build.
	if _, err := fs.Stat(fsys, ".keep"); err != nil {
		if _, err := fs.Stat(fsys, "index.html"); err != nil {
			t.Fatalf("dist has neither .keep nor index.html: %v", err)
		}
	}
	if HasIndex() != HasIndexFS(fsys) {
		t.Fatal("HasIndex disagrees with HasIndexFS(FS())")
	}
}

func TestHasIndexFS(t *testing.T) {
	if HasIndexFS(nil) || HasIndexFS(fstest.MapFS{".keep": {}}) {
		t.Fatal("no index.html must report false")
	}
	if HasIndexFS(fstest.MapFS{"index.html/x": {}}) {
		t.Fatal("a folder called index.html is not an index")
	}
	if !HasIndexFS(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}) {
		t.Fatal("index.html must report true")
	}
}

func TestFallbackHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	FallbackHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("status %d, headers %v", rec.Code, rec.Header())
	}
	if !strings.Contains(rec.Body.String(), "build:app") {
		t.Fatal("the fallback page must say how to build the frontend")
	}
	rec = httptest.NewRecorder()
	FallbackHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Body.Len() != 0 {
		t.Fatal("HEAD must not have a body")
	}
}
