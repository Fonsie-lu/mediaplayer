package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mediaplayer/internal/session"
)

// testMux is a Handler wired to an in-memory config and an idle session
// manager (no reaper, no ffmpeg), registered on a real ServeMux.
//
// It holds only Register's patterns, which is what main.go's mux must also
// look like for these expectations to hold: a bare "/" catch-all there would
// match /api/* first and turn every method mismatch into the file server's
// 404, since ServeMux synthesises 405 only when nothing matched at all.
func testMux(t *testing.T) (*http.ServeMux, *session.Manager) {
	t.Helper()
	mgr := session.NewManager()
	h := &Handler{Cfg: testCfg(), Sessions: mgr}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, mgr
}

func status(t *testing.T, mux *http.ServeMux, method, target string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec.Code
}

// Methods are part of the route patterns rather than something each handler
// checks, so the mux is what has to reject the wrong verb.
func TestRouteMethods(t *testing.T) {
	mux, _ := testMux(t)
	cases := []struct {
		method, target string
		want           int
	}{
		{"GET", "/api/rename", http.StatusMethodNotAllowed},
		{"GET", "/api/delete", http.StatusMethodNotAllowed},
		{"GET", "/api/stars/toggle", http.StatusMethodNotAllowed},
		{"GET", "/api/stream/close", http.StatusMethodNotAllowed},
		{"POST", "/api/mounts", http.StatusMethodNotAllowed},
		{"POST", "/api/browse", http.StatusMethodNotAllowed},
		{"GET", "/api/mounts", http.StatusOK},
		// GET patterns must keep matching HEAD: http.ServeFile answers those
		// on the file-serving routes.
		{"HEAD", "/api/mounts", http.StatusOK},
	}
	for _, c := range cases {
		if got := status(t, mux, c.method, c.target); got != c.want {
			t.Errorf("%s %s: status %d, want %d", c.method, c.target, got, c.want)
		}
	}
}

// The HLS route takes {sid}/{file} from the pattern; these are the three ways
// that can go wrong without ffmpeg being involved.
func TestStreamHLSRouting(t *testing.T) {
	mux, mgr := testMux(t)

	if got := status(t, mux, "GET", "/api/stream/hls/unknown/playlist.m3u8"); got != http.StatusNotFound {
		t.Errorf("unknown session: status %d, want 404", got)
	}

	sid, _ := mgr.Add(&session.Session{Duration: 8, Dir: t.TempDir()})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/stream/hls/"+sid+"/playlist.m3u8", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("playlist: status %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "seg_00000.ts") {
		t.Errorf("playlist body has no segment line:\n%s", body)
	}

	if got := status(t, mux, "GET", "/api/stream/hls/"+sid+"/notasegment.ts"); got != http.StatusBadRequest {
		t.Errorf("bad segment name: status %d, want 400", got)
	}

	// A path that tries to climb out is resolved by the mux before matching,
	// so it never reaches the handler as a {file} value.
	if got := status(t, mux, "GET", "/api/stream/hls/"+sid+"/../../etc/passwd"); got == http.StatusOK {
		t.Error("traversal attempt returned 200")
	}
}

// Every open is a session of its own, closed by its ID: closing one must leave
// another page's session playing (they used to share one cookie-keyed slot).
func TestStreamCloseBySessionID(t *testing.T) {
	mux, mgr := testMux(t)
	a, _ := mgr.Add(&session.Session{Duration: 8, Dir: t.TempDir()})
	b, _ := mgr.Add(&session.Session{Duration: 8, Dir: t.TempDir()})
	if a == b {
		t.Fatalf("two sessions share ID %q", a)
	}

	if got := status(t, mux, "POST", "/api/stream/close"); got != http.StatusBadRequest {
		t.Errorf("close without session: status %d, want 400", got)
	}
	if got := status(t, mux, "POST", "/api/stream/close?session="+a); got != http.StatusOK {
		t.Errorf("close: status %d, want 200", got)
	}
	if got := status(t, mux, "POST", "/api/stream/close?session="+a); got != http.StatusOK {
		t.Errorf("second close: status %d, want 200", got)
	}
	if got := status(t, mux, "GET", "/api/stream/hls/"+a+"/playlist.m3u8"); got != http.StatusNotFound {
		t.Errorf("closed session: status %d, want 404", got)
	}
	if got := status(t, mux, "GET", "/api/stream/hls/"+b+"/playlist.m3u8"); got != http.StatusOK {
		t.Errorf("other session after close: status %d, want 200", got)
	}
}

// A cross-site page can only send "simple" requests without a preflight this
// server never answers — so a mutating endpoint must refuse any body that
// isn't declared JSON, or that page could delete files.
func TestMutatingEndpointsRequireJSON(t *testing.T) {
	mux, _ := testMux(t)
	for _, target := range []string{"/api/rename", "/api/delete", "/api/stars/toggle", "/api/config"} {
		for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
			req := httptest.NewRequest("POST", target, strings.NewReader(`{"mount":"0","path":"x"}`))
			if ct != "" {
				req.Header.Set("Content-Type", ct)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("POST %s as %q: status %d, want 415", target, ct, rec.Code)
			}
		}
	}
}
