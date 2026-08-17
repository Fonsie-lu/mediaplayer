package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A config path that doesn't exist is not an error: Load writes the defaults
// there and returns them, so a first run comes up and creates its own file.
func TestLoadMissingFileWritesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mediaplayer.json")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Addr(); got != "0.0.0.0:8090" {
		t.Errorf("Addr = %q, want the default 0.0.0.0:8090", got)
	}
	// Including the parent directory, which XDG paths often lack.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Load did not create %s: %v", path, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"host"`) {
		t.Errorf("written config missing host:\n%s", raw)
	}
}

// A file that omits host/port must keep the defaults rather than binding :0.
func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(path, []byte(`{"mounts":[{"name":"m","path":"/srv/m"}]}`), 0644)

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Addr(); got != "0.0.0.0:8090" {
		t.Errorf("Addr = %q, want the defaults to survive a partial file", got)
	}
	if got := c.Snapshot().Mounts; len(got) != 1 || got[0].Name != "m" {
		t.Errorf("Mounts = %v, want the one from the file", got)
	}
}

func TestLoadNormalizesMounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	// A trailing slash must be gone before any handler sees it, and more than
	// MaxMounts entries are truncated to the number of positional keybinds.
	var mounts []Mount
	mounts = append(mounts, Mount{Name: "trail", Path: "/srv/media/"})
	for i := 0; i < MaxMounts+5; i++ {
		mounts = append(mounts, Mount{Name: "m", Path: "/srv/x"})
	}
	raw, _ := json.Marshal(Snapshot{Mounts: mounts})
	os.WriteFile(path, raw, 0644)

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := c.Snapshot().Mounts
	if len(got) != MaxMounts {
		t.Errorf("len(Mounts) = %d, want %d", len(got), MaxMounts)
	}
	if got[0].Path != "/srv/media" {
		t.Errorf("Mounts[0].Path = %q, want the trailing slash cleaned", got[0].Path)
	}
}

// Replace only touches mounts, so the disk setting must survive it — but Save
// marshals the whole struct, so the file gains every key.
func TestReplacePreservesOtherSettingsAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(path, []byte(`{"host":"127.0.0.1","port":9000,"disk":"/dev/sda1"}`), 0644)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := c.Replace([]Mount{{Name: "new", Path: "/srv/new/"}}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := c.DiskSpec(); got != "/dev/sda1" {
		t.Errorf("DiskSpec = %q, want it to survive Replace", got)
	}
	if got := c.Addr(); got != "127.0.0.1:9000" {
		t.Errorf("Addr = %q, want it to survive Replace", got)
	}
	if got := c.Snapshot().Mounts[0].Path; got != "/srv/new" {
		t.Errorf("Replace didn't normalize: %q", got)
	}

	// And it reached disk: a reload sees the same thing.
	again, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := again.Snapshot().Mounts; len(got) != 1 || got[0].Name != "new" {
		t.Errorf("reloaded mounts = %v, want the replaced one", got)
	}
	if got := again.DiskSpec(); got != "/dev/sda1" {
		t.Errorf("reloaded DiskSpec = %q, want /dev/sda1", got)
	}
}

// A handed-out Snapshot must not alias the live mounts slice: handlers range
// over it while the TUI replaces mounts underneath.
func TestSnapshotDoesNotAlias(t *testing.T) {
	c := New(Snapshot{Mounts: []Mount{{Name: "a", Path: "/a"}}})
	snap := c.Snapshot()
	snap.Mounts[0].Name = "mutated"

	if got := c.Snapshot().Mounts[0].Name; got != "a" {
		t.Errorf("mutating a snapshot changed the config: %q", got)
	}

	// The other direction too: the slice New was given must not stay live.
	src := []Mount{{Name: "b", Path: "/b"}}
	c2 := New(Snapshot{Mounts: src})
	src[0].Name = "mutated"
	if got := c2.Snapshot().Mounts[0].Name; got != "b" {
		t.Errorf("New aliased its argument: %q", got)
	}
}

func TestMountByIndexBounds(t *testing.T) {
	c := New(Snapshot{Mounts: []Mount{{Name: "a", Path: "/a"}, {Name: "b", Path: "/b"}}})
	if m, ok := c.MountByIndex(1); !ok || m.Name != "b" {
		t.Errorf("MountByIndex(1) = %v, %v", m, ok)
	}
	for _, i := range []int{-1, 2, 99} {
		if _, ok := c.MountByIndex(i); ok {
			t.Errorf("MountByIndex(%d) = ok, want false", i)
		}
	}
}

// New's config has no file, so Save must be a no-op rather than trying to write
// to "" and failing every Replace.
func TestInMemoryConfigSaveIsNoop(t *testing.T) {
	c := New(Snapshot{})
	if err := c.Replace([]Mount{{Name: "a", Path: "/a"}}); err != nil {
		t.Errorf("Replace on an in-memory config: %v", err)
	}
	if _, err := os.Stat(""); err == nil {
		t.Error("a file named \"\" exists, which makes this test meaningless")
	}
}

func TestLoadRejectsBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(path, []byte("{not json"), 0644)
	if _, err := Load(path); err == nil {
		t.Error("Load accepted invalid JSON")
	} else if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q should name the file it failed to parse", err)
	}
}
