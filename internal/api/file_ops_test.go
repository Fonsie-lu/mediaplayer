package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediaplayer/internal/config"
	"mediaplayer/internal/session"
)

// fileOpsMux serves one real mount, "m", rooted at a temp dir, with a star
// store, and returns the mount root.
func fileOpsMux(t *testing.T) (*http.ServeMux, *StarStore, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.New(config.Snapshot{Mounts: []config.Mount{{Name: "m", Path: root}}})
	stars, err := NewStarStore(filepath.Join(t.TempDir(), "stars.json"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{Cfg: cfg, Sessions: session.NewManager(), Stars: stars}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, stars, root
}

func postJSON(mux *http.ServeMux, target, body string) int {
	req := httptest.NewRequest("POST", target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRenameRefusesToOverwrite(t *testing.T) {
	mux, _, root := fileOpsMux(t)
	touch(t, filepath.Join(root, "a.mkv"))
	touch(t, filepath.Join(root, "b.mkv"))

	if got := postJSON(mux, "/api/rename", `{"mount":"0","path":"a.mkv","new_name":"b.mkv"}`); got != http.StatusConflict {
		t.Errorf("rename onto a sibling: status %d, want 409", got)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "b.mkv")); string(data) != "b.mkv" {
		t.Errorf("b.mkv was overwritten: %q", data)
	}
	if got := postJSON(mux, "/api/rename", `{"mount":"0","path":"a.mkv","new_name":"c.mkv"}`); got != http.StatusOK {
		t.Errorf("rename to a free name: status %d, want 200", got)
	}
}

func TestDeleteRefusesMountRootSpellings(t *testing.T) {
	mux, _, root := fileOpsMux(t)
	touch(t, filepath.Join(root, "keep.mkv"))
	for _, p := range []string{"", ".", "x/..", "/"} {
		if got := postJSON(mux, "/api/delete", `{"mount":"0","path":"`+p+`"}`); got == http.StatusOK {
			t.Errorf("delete %q: status 200, want a refusal", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "keep.mkv")); err != nil {
		t.Fatalf("mount content gone: %v", err)
	}
}

// A star follows its entry through a rename (including entries below a
// renamed folder) and goes away with a delete.
func TestStarsFollowRenameAndDelete(t *testing.T) {
	mux, stars, root := fileOpsMux(t)
	touch(t, filepath.Join(root, "show", "ep1.mkv"))
	touch(t, filepath.Join(root, "showcase.mkv"))
	for _, p := range []string{"show", "show/ep1.mkv", "showcase.mkv"} {
		if got := postJSON(mux, "/api/stars/toggle", `{"mount":"0","path":"`+p+`"}`); got != http.StatusOK {
			t.Fatalf("star %s: status %d", p, got)
		}
	}

	if got := postJSON(mux, "/api/rename", `{"mount":"0","path":"show","new_name":"series"}`); got != http.StatusOK {
		t.Fatalf("rename: status %d", got)
	}
	want := map[string]bool{"series": true, "series/ep1.mkv": true, "showcase.mkv": true}
	for _, ref := range stars.List() {
		if !want[ref.Path] {
			t.Errorf("unexpected star %q after rename", ref.Path)
		}
		delete(want, ref.Path)
	}
	for p := range want {
		t.Errorf("star %q missing after rename", p)
	}

	if got := postJSON(mux, "/api/delete", `{"mount":"0","path":"series"}`); got != http.StatusOK {
		t.Fatalf("delete: status %d", got)
	}
	if refs := stars.List(); len(refs) != 1 || refs[0].Path != "showcase.mkv" {
		t.Errorf("stars after delete = %v, want only showcase.mkv", refs)
	}
}
