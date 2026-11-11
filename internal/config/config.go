package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Mount struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Identity is the stable key stars are stored under: the mount's name, or its
// path when it has none. Not the index — indices renumber whenever mounts are
// reordered or one is deleted.
func (m Mount) Identity() string {
	if m.Name != "" {
		return m.Name
	}
	return m.Path
}

// Snapshot is the config's data: the on-disk JSON shape, and a pointer-free
// value handlers can read without holding a lock.
//
// There is deliberately only this one field list. Config used to carry the
// same four fields itself and hand out a separate Snapshot struct that mirrored
// them, which meant adding a setting meant editing two structs and a copy
// function, and forgetting the third made the new field read as its zero value
// through Snapshot() while being perfectly present on disk.
type Snapshot struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// Disk is the filesystem the browser's free-space widget reports on:
	// either a device node (/dev/nvme0n1p3, resolved to its mountpoint) or a
	// directory. Empty disables the widget.
	Disk   string  `json:"disk"`
	Mounts []Mount `json:"mounts"`
}

// clone deep-copies the mounts slice so a handed-out Snapshot can never alias
// the live one — a caller ranging over mounts while the TUI replaces them would
// otherwise race on the backing array.
func (s Snapshot) clone() Snapshot {
	s.Mounts = append([]Mount(nil), s.Mounts...)
	return s
}

// Config is the live, mutable config: a Snapshot behind a lock, plus the file
// it persists to. Mount edits are applied at runtime (the TUI and /api/config
// both call Replace), so handlers must read through Snapshot()/MountByIndex()
// and never cache what they get.
type Config struct {
	mu   sync.RWMutex
	data Snapshot
	file string
	// saveMu orders whole saves (marshal + write), so two concurrent Replaces
	// can't land on disk in the opposite order to the one they took effect in.
	// Separate from mu so readers never wait on disk I/O.
	saveMu sync.Mutex
}

const MaxMounts = 10

// New builds an in-memory config backed by no file, for callers that have the
// settings already (tests, and anything wiring a config up by hand). Save is a
// no-op on it, so Replace mutates without persisting.
func New(data Snapshot) *Config {
	data = data.clone()
	data.Mounts = normalizeMounts(data.Mounts)
	return &Config{data: data}
}

func Load(path string) (*Config, error) {
	c := &Config{
		data: Snapshot{Host: "0.0.0.0", Port: 8090},
		file: path,
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Not an error: write the defaults and come up on an empty browser
			// page, so a first run creates the file it was pointed at.
			return c, c.Save()
		}
		return nil, err
	}
	// Unmarshal onto the defaults, so a file that omits host/port keeps them.
	if err := json.Unmarshal(raw, &c.data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.data.Mounts = normalizeMounts(c.data.Mounts)
	return c, nil
}

// normalizeMounts enforces what every entry point into the mounts slice must
// guarantee the rest of the program: at most MaxMounts of them (the number of
// positional keybinds) and no trailing separators, so a handler never has to
// Clean a mount path itself. Both the config file and a live Replace go
// through here.
//
// It returns a copy, so the caller's slice is never aliased by the live config.
func normalizeMounts(mounts []Mount) []Mount {
	if len(mounts) > MaxMounts {
		mounts = mounts[:MaxMounts]
	}
	mounts = append([]Mount(nil), mounts...)
	for i := range mounts {
		mounts[i].Path = filepath.Clean(mounts[i].Path)
	}
	return mounts
}

// Save writes the whole config, so any save adds keys the file may have
// lacked (a config with no "disk" gains an empty one).
func (c *Config) Save() error {
	if c.file == "" {
		return nil // in-memory config (see New)
	}
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	c.mu.RLock()
	data, err := json.MarshalIndent(c.data, "", "  ")
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	return WriteFileAtomic(c.file, append(data, '\n'), 0o644)
}

// WriteFileAtomic replaces path with data so that a reader — or the next
// start after a crash mid-write — sees either the old content or the new,
// never a truncated file. os.WriteFile truncates first, and a config cut short
// there fails the next start with "unexpected end of JSON input". Missing
// parent directories are created.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once renamed
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Snapshot is a lock-free, pointer-free view of the config for reading.
func (c *Config) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.clone()
}

// Replace swaps the mount list and persists. Only mounts are touched, so the
// other settings survive it.
func (c *Config) Replace(mounts []Mount) error {
	c.mu.Lock()
	c.data.Mounts = normalizeMounts(mounts)
	c.mu.Unlock()
	return c.Save()
}

// Addr is the listen address the server binds.
func (c *Config) Addr() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return fmt.Sprintf("%s:%d", c.data.Host, c.data.Port)
}

// DiskSpec reads the one field /api/disk needs, without Snapshot's copy of the
// whole mounts slice — the widget polls once a minute per open tab.
func (c *Config) DiskSpec() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.Disk
}

func (c *Config) MountByIndex(i int) (Mount, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if i < 0 || i >= len(c.data.Mounts) {
		return Mount{}, false
	}
	return c.data.Mounts[i], true
}
