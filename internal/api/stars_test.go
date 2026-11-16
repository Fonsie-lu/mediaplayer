package api

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"mediaplayer/internal/config"
)

func testCfg(names ...string) *config.Config {
	var mounts []config.Mount
	for _, n := range names {
		mounts = append(mounts, config.Mount{Name: n, Path: "/srv/" + n})
	}
	return config.New(config.Snapshot{Mounts: mounts})
}

func readStarFile(t *testing.T, path string) []StarRef {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read star file: %v", err)
	}
	var refs []StarRef
	if err := json.Unmarshal(data, &refs); err != nil {
		t.Fatalf("parse star file: %v", err)
	}
	return refs
}

// The whole point of the change: a star must survive its mount being
// renumbered. Written under index 1, read back after the mounts are reordered,
// it still names the same directory.
func TestStarsSurviveMountReorder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stars.json")
	cfg := testCfg("movies", "tv")
	store, err := NewStarStore(path, cfg)
	if err != nil {
		t.Fatalf("NewStarStore: %v", err)
	}
	// Star something in "tv", which is index 1 today.
	if _, err := store.toggle(StarRef{Mount: "tv", Path: "/s01/e01.mkv"}); err != nil {
		t.Fatalf("toggle: %v", err)
	}

	// Reorder: tv is now index 0.
	if err := cfg.Replace([]config.Mount{{Name: "tv", Path: "/srv/tv"}, {Name: "movies", Path: "/srv/movies"}}); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	got := store.List()
	if len(got) != 1 || got[0].Mount != "tv" {
		t.Fatalf("List = %v, want the star still on tv", got)
	}
	if idx := store.indexOf("tv"); idx != "0" {
		t.Errorf("indexOf(tv) = %q, want the new index 0", idx)
	}
	if idx := store.indexOf("movies"); idx != "1" {
		t.Errorf("indexOf(movies) = %q, want 1", idx)
	}
}

// Existing star files hold indices. They must be converted on load, and the
// conversion must reach disk so a later reorder can't re-break them.
func TestStarsMigrateLegacyIndices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stars.json")
	legacy := `[{"mount":"0","path":"/a.mkv"},{"mount":"1","path":"/b.mkv"}]`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewStarStore(path, testCfg("movies", "tv"))
	if err != nil {
		t.Fatalf("NewStarStore: %v", err)
	}
	got := store.List()
	if len(got) != 2 {
		t.Fatalf("List = %v, want 2 entries", got)
	}
	if got[0].Mount != "movies" || got[0].Path != "/a.mkv" {
		t.Errorf("got[0] = %+v, want mount movies", got[0])
	}
	if got[1].Mount != "tv" || got[1].Path != "/b.mkv" {
		t.Errorf("got[1] = %+v, want mount tv", got[1])
	}

	// Durable: the file now holds names.
	onDisk := readStarFile(t, path)
	for _, r := range onDisk {
		if r.Mount == "0" || r.Mount == "1" {
			t.Errorf("migration not persisted, file still holds indices: %+v", onDisk)
		}
	}
}

// An index naming no current mount is already orphaned. Guessing would attach
// the star to the wrong directory, so it must be left exactly as it was — and
// still be listed, so it can be removed.
func TestStarsLeaveUnresolvableIndexAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stars.json")
	if err := os.WriteFile(path, []byte(`[{"mount":"7","path":"/a.mkv"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewStarStore(path, testCfg("movies"))
	if err != nil {
		t.Fatalf("NewStarStore: %v", err)
	}
	got := store.List()
	if len(got) != 1 || got[0].Mount != "7" {
		t.Fatalf("List = %v, want the orphan preserved verbatim", got)
	}
	if idx := store.indexOf("7"); idx != "" {
		t.Errorf("indexOf = %q, want empty for a mount that doesn't exist", idx)
	}
	// Removable despite being orphaned.
	if err := store.Remove(got[0]); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(store.List()) != 0 {
		t.Error("orphaned star could not be removed")
	}
}

// A file already in the new format must not be touched by the migration.
func TestStarsNoMigrationForNamedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stars.json")
	if err := os.WriteFile(path, []byte(`[{"mount":"movies","path":"/a.mkv"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStarStore(path, testCfg("movies"))
	if err != nil {
		t.Fatalf("NewStarStore: %v", err)
	}
	if got := store.List(); len(got) != 1 || got[0].Mount != "movies" {
		t.Errorf("List = %v", got)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.ModTime() != after.ModTime() || before.Size() != after.Size() {
		t.Error("file rewritten even though nothing needed migrating")
	}
}

func TestStarsToggleRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stars.json")
	store, err := NewStarStore(path, testCfg("movies"))
	if err != nil {
		t.Fatal(err)
	}
	ref := StarRef{Mount: "movies", Path: "/a.mkv"}

	if on, err := store.toggle(ref); err != nil || !on {
		t.Fatalf("first toggle = %v, %v; want true", on, err)
	}
	if on, err := store.toggle(ref); err != nil || on {
		t.Fatalf("second toggle = %v, %v; want false", on, err)
	}
	if len(store.List()) != 0 {
		t.Error("toggle off left the entry behind")
	}
	// Toggling off an absent entry must not resurrect it on disk.
	if refs := readStarFile(t, path); len(refs) != 0 {
		t.Errorf("file = %v, want empty", refs)
	}
}

// A missing star file is a normal first run, not an error.
func TestStarsMissingFileStartsEmpty(t *testing.T) {
	store, err := NewStarStore(filepath.Join(t.TempDir(), "nope.json"), testCfg("movies"))
	if err != nil {
		t.Fatalf("NewStarStore on a missing file: %v", err)
	}
	if got := store.List(); len(got) != 0 {
		t.Errorf("List = %v, want empty", got)
	}
}

// A first run may have no config directory at all (fresh account, or
// XDG_CONFIG_HOME pointed somewhere that doesn't exist yet). Reading a missing
// star file is a supported empty start, so nothing else creates that directory —
// save has to, or every toggle fails with a 500.
func TestStarsSaveCreatesConfigDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does", "not", "exist", "stars.json")
	store, err := NewStarStore(path, testCfg("movies"))
	if err != nil {
		t.Fatalf("NewStarStore: %v", err)
	}
	if _, err := store.toggle(StarRef{Mount: "movies", Path: "/a.mkv"}); err != nil {
		t.Fatalf("toggle into a missing config dir: %v", err)
	}
	if got := readStarFile(t, path); len(got) != 1 {
		t.Errorf("star file = %v, want the one entry", got)
	}
}

// Stars are keyed by mount name, so renaming a mount used to orphan every star
// under it. The directory is what ties the old name to the new one.
func TestStarsFollowMountRename(t *testing.T) {
	cfg := testCfg("films", "series")
	path := filepath.Join(t.TempDir(), "stars.json")
	s, err := NewStarStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []StarRef{{"films", "a.mkv"}, {"films", "dir/b.mkv"}, {"series", "c.mkv"}} {
		if _, err := s.toggle(ref); err != nil {
			t.Fatal(err)
		}
	}

	before := cfg.Snapshot().Mounts
	after := []config.Mount{{Name: "movies", Path: "/srv/films"}, before[1]}
	if err := s.FollowMounts(before, after); err != nil {
		t.Fatal(err)
	}
	want := []StarRef{{"movies", "a.mkv"}, {"movies", "dir/b.mkv"}, {"series", "c.mkv"}}
	if got := s.List(); !slices.Equal(got, want) {
		t.Errorf("after rename: %v, want %v", got, want)
	}
	if got := readStarFile(t, path); !slices.Equal(got, want) {
		t.Errorf("on disk: %v, want %v", got, want)
	}
}

func TestRenamedMounts(t *testing.T) {
	m := func(name, path string) config.Mount { return config.Mount{Name: name, Path: path} }
	cases := []struct {
		name          string
		before, after []config.Mount
		want          map[string]string
	}{
		{"rename", []config.Mount{m("a", "/x")}, []config.Mount{m("b", "/x")}, map[string]string{"a": "b"}},
		{"reorder only", []config.Mount{m("a", "/x"), m("b", "/y")}, []config.Mount{m("b", "/y"), m("a", "/x")}, nil},
		{"path edit keeps the name", []config.Mount{m("a", "/x")}, []config.Mount{m("a", "/z")}, nil},
		{"name and path both changed", []config.Mount{m("a", "/x")}, []config.Mount{m("b", "/z")}, nil},
		{"deleted", []config.Mount{m("a", "/x"), m("b", "/y")}, []config.Mount{m("b", "/y")}, nil},
		// The other mount on /x was there before: it is not a new name for a.
		{"deleted beside a twin", []config.Mount{m("a", "/x"), m("b", "/x")}, []config.Mount{m("b", "/x")}, nil},
		{"two new names on the path", []config.Mount{m("a", "/x")}, []config.Mount{m("b", "/x"), m("c", "/x")}, nil},
		// An unnamed mount's identity is its path; naming it is a rename.
		{"unnamed gets a name", []config.Mount{m("", "/x")}, []config.Mount{m("b", "/x")}, map[string]string{"/x": "b"}},
	}
	for _, c := range cases {
		got := renamedMounts(c.before, c.after)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !maps.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// pruneStore is a star store over two real mount dirs, "m" and "other", and
// an identity ("left") that no configured mount carries.
func pruneStore(t *testing.T, refs ...StarRef) (s *StarStore, root, file string) {
	t.Helper()
	root = t.TempDir()
	cfg := config.New(config.Snapshot{Mounts: []config.Mount{
		{Name: "m", Path: root},
		{Name: "other", Path: t.TempDir()},
	}})
	file = filepath.Join(t.TempDir(), "stars.json")
	s, err := NewStarStore(file, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if _, err := s.toggle(ref); err != nil {
			t.Fatal(err)
		}
	}
	return s, root, file
}

// A file deleted behind the server's back takes its star with it at the next
// start; everything that still exists, or can't be checked, keeps its own.
func TestStarsPruneMissing(t *testing.T) {
	s, root, file := pruneStore(t,
		StarRef{"m", "kept.mkv"},
		StarRef{"m", "deleted.mkv"},
		StarRef{"m", "show"},
		StarRef{"m", "show/ep1.mkv"},
		StarRef{"m", "now-a-file/ep1.mkv"},
		StarRef{"m", "dangling"},
		StarRef{"left", "a.mkv"},
	)
	touch(t, filepath.Join(root, "kept.mkv"))
	touch(t, filepath.Join(root, "now-a-file"))
	if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}

	if got := s.PruneMissing(); got != 4 {
		t.Errorf("PruneMissing() = %d, want 4", got)
	}
	want := []StarRef{{"left", "a.mkv"}, {"m", "dangling"}, {"m", "kept.mkv"}}
	if got := s.List(); !slices.Equal(got, want) {
		t.Errorf("after prune: %v, want %v", got, want)
	}
	if got := readStarFile(t, file); !slices.Equal(got, want) {
		t.Errorf("on disk: %v, want %v", got, want)
	}
	if got := s.PruneMissing(); got != 0 {
		t.Errorf("second PruneMissing() = %d, want 0", got)
	}
}

// A disk that isn't mounted yet looks like a mount whose files were all
// deleted. Its stars must outlive that: an empty mountpoint proves nothing,
// whether it is the mount root, a directory below it, or missing altogether.
func TestStarsPruneSparesAnUnmountedDisk(t *testing.T) {
	refs := []StarRef{{"m", "film.mkv"}, {"m", "dir/film.mkv"}, {"other", "disk1/film.mkv"}}
	s, root, file := pruneStore(t, refs...)
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}

	// "m" is an empty directory; "other" has content beside an empty disk1.
	other := s.cfg.Snapshot().Mounts[1].Path
	touch(t, filepath.Join(other, "disk2", "film.mkv"))
	if err := os.Mkdir(filepath.Join(other, "disk1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := s.PruneMissing(); got != 0 {
		t.Errorf("PruneMissing() over empty mountpoints = %d, want 0", got)
	}

	// The mount path itself gone.
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if got := s.PruneMissing(); got != 0 {
		t.Errorf("PruneMissing() over a missing mount path = %d, want 0", got)
	}

	want := []StarRef{{"m", "dir/film.mkv"}, {"m", "film.mkv"}, {"other", "disk1/film.mkv"}}
	if got := s.List(); !slices.Equal(got, want) {
		t.Errorf("stars = %v, want all of %v", got, want)
	}
	if after, err := os.Stat(file); err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("star file was rewritten though nothing was pruned (err %v)", err)
	}
}
