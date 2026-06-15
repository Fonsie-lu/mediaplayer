package api

import (
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

func TestResolveMount(t *testing.T) {
	cfg := &config.Config{Mounts: []config.Mount{
		{Name: "movies", Path: "/srv/movies"},
		{Name: "tv", Path: "/srv/tv"},
	}}

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
