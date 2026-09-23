package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"mediaplayer/internal/api"
	"mediaplayer/internal/config"
	"mediaplayer/internal/session"
)

func testServerMux(t *testing.T) *http.ServeMux {
	t.Helper()
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	h := &api.Handler{Cfg: config.New(config.Snapshot{}), Sessions: session.NewManager()}
	return newMux(h, web)
}

// The API's method enforcement lives entirely in its route patterns, and a
// catch-all pattern here would defeat it: ServeMux synthesises 405 only when
// nothing matched, so a bare "/" for the file server would answer every
// wrong-verb API request with a 404 from the embedded assets instead. That
// regression is invisible from internal/api, which is why this test is here.
func TestAPIMethodsNotShadowedByStaticRoutes(t *testing.T) {
	mux := testServerMux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/rename", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/rename: status %d, want 405 — a catch-all route is shadowing /api/", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "POST" {
		t.Errorf("GET /api/rename: Allow %q, want POST", allow)
	}
}

func TestStaticRoutes(t *testing.T) {
	mux := testServerMux(t)
	// Each page is served by rewriting to its embedded file, so a 200 here also
	// proves the rewrite still points at a file that exists.
	for _, target := range []string{"/", "/player", "/css/tokyo-night.css", "/js/api.js"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", target, rec.Code)
		}
	}
	// Nothing else is served: no catch-all means an unknown path is a 404 and
	// the .html files are reachable only through their page routes.
	for _, target := range []string{"/nope", "/browser.html", "/../go.mod"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s: status 200, want a miss", target)
		}
	}
}

// Embedded files carry no modification time, so without an ETag a browser can
// only re-download them; with one, a page hop revalidates to a 304.
func TestStaticAssetsRevalidate(t *testing.T) {
	mux := testServerMux(t)
	for _, target := range []string{"/", "/player", "/js/player.js", "/css/browser.css"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		tag := rec.Header().Get("ETag")
		if tag == "" {
			t.Errorf("GET %s: no ETag", target)
			continue
		}
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("If-None-Match", tag)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Errorf("GET %s with its own ETag: status %d, want 304", target, rec.Code)
		}
	}
	// The two pages are different files and must not share a validator.
	tags := map[string]string{}
	for _, target := range []string{"/", "/player"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		tags[target] = rec.Header().Get("ETag")
	}
	if tags["/"] == tags["/player"] {
		t.Error("browser and player pages share an ETag")
	}
}
