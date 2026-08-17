package api

import (
	"encoding/json"
	"os"
	"path/filepath"
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
