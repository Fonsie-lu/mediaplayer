package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediaplayer/internal/config"
)

func TestSafeJoin(t *testing.T) {
	cases := []struct {
		rel  string
		want string
	}{
		{"", "/srv/media"},
		{"movies", "/srv/media/movies"},
		{"movies/a.mkv", "/srv/media/movies/a.mkv"},
		{"../../../etc/passwd", "/srv/media/etc/passwd"},
		{"..", "/srv/media"},
		{"./a/../b", "/srv/media/b"},
		{"/abs/path", "/srv/media/abs/path"},
	}
	for _, c := range cases {
		got, err := safeJoin("/srv/media", c.rel)
		if err != nil {
			t.Errorf("safeJoin(%q) error: %v", c.rel, err)
			continue
		}
		if got != c.want {
			t.Errorf("safeJoin(%q) = %q, want %q", c.rel, got, c.want)
		}
	}
}

// TestSafeJoinSymlinks builds a real tree, since symlink escapes are exactly
// what the lexical Clean check cannot see:
//
//	root/sub/a.mkv        plain content
//	root/inside  -> sub   link that stays in the mount
//	root/escape  -> out   link out of the mount (dir)
//	root/leak.txt-> out/secret.txt
//	rootlink     -> root  the mount root is itself a link
func TestSafeJoinSymlinks(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	out := filepath.Join(tmp, "out")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(root, "sub", "a.mkv"), filepath.Join(out, "secret.txt")} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	links := []struct{ target, name string }{
		{filepath.Join(root, "sub"), filepath.Join(root, "inside")},
		{out, filepath.Join(root, "escape")},
		{filepath.Join(out, "secret.txt"), filepath.Join(root, "leak.txt")},
		{root, filepath.Join(tmp, "rootlink")},
	}
	for _, l := range links {
		if err := os.Symlink(l.target, l.name); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name    string
		root    string
		rel     string
		wantErr bool
	}{
		{"plain file", root, "sub/a.mkv", false},
		{"link inside the mount", root, "inside/a.mkv", false},
		{"nonexistent rename destination", root, "sub/renamed.mkv", false},
		{"nonexistent nested dir", root, "sub/deep/deeper.mkv", false},
		{"dir link out of the mount", root, "escape", true},
		{"file through a dir link out", root, "escape/secret.txt", true},
		{"file link out of the mount", root, "leak.txt", true},
		// Clean collapses ".." lexically, before any link is traversed, so this
		// never reaches the escaping link at all — it names root/out, inside
		// the mount. Unlike the OS, which would resolve escape/ first and land
		// in tmp/. Collapsing first is what makes the pair of checks airtight.
		{"dotdot past an escaping link collapses inside", root, "escape/../out", false},
		{"mount root is itself a link", filepath.Join(tmp, "rootlink"), "sub/a.mkv", false},
		{"escape via a linked mount root", filepath.Join(tmp, "rootlink"), "leak.txt", true},
	}
	for _, c := range cases {
		got, err := safeJoin(c.root, c.rel)
		switch {
		case c.wantErr && err == nil:
			t.Errorf("%s: safeJoin(%q, %q) = %q, want ErrTraversal", c.name, c.root, c.rel, got)
		case c.wantErr && !errors.Is(err, ErrTraversal):
			t.Errorf("%s: got %v, want ErrTraversal", c.name, err)
		case !c.wantErr && err != nil:
			t.Errorf("%s: safeJoin(%q, %q) error: %v", c.name, c.root, c.rel, err)
		case !c.wantErr && !strings.HasPrefix(got, filepath.Clean(c.root)+string(filepath.Separator)):
			// The returned path stays unresolved and under the given root:
			// del compares it against mount.Path and rename rebuilds siblings
			// from it, so resolving it here would break both.
			t.Errorf("%s: safeJoin(%q, %q) = %q, want it under the root", c.name, c.root, c.rel, got)
		}
	}
}

func TestResolveMount(t *testing.T) {
	cfg := config.New(config.Snapshot{Mounts: []config.Mount{
		{Name: "movies", Path: "/srv/movies"},
		{Name: "tv", Path: "/srv/tv"},
	}})

	if m, err := resolveMount(cfg, "0"); err != nil || m.Name != "movies" {
		t.Errorf("by index 0: got %v, %v", m, err)
	}
	if m, err := resolveMount(cfg, "1"); err != nil || m.Name != "tv" {
		t.Errorf("by index 1: got %v, %v", m, err)
	}
	if m, err := resolveMount(cfg, "tv"); err != nil || m.Path != "/srv/tv" {
		t.Errorf("by name: got %v, %v", m, err)
	}
	if _, err := resolveMount(cfg, "5"); err == nil {
		t.Error("out-of-range index should fail")
	}
	if _, err := resolveMount(cfg, "nope"); err == nil {
		t.Error("unknown name should fail")
	}
}
