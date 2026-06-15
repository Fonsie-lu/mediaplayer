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

type Config struct {
	Host   string  `json:"host"`
	Port   int     `json:"port"`
	Mounts []Mount `json:"mounts"`

	file string
	mu   sync.RWMutex
}

const MaxMounts = 10

func Load(path string) (*Config, error) {
	c := &Config{
		Host: "0.0.0.0",
		Port: 8090,
		file: path,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, c.Save()
		}
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.file = path
	if len(c.Mounts) > MaxMounts {
		c.Mounts = c.Mounts[:MaxMounts]
	}
	for i := range c.Mounts {
		c.Mounts[i].Path = filepath.Clean(c.Mounts[i].Path)
	}
	return c, nil
}

func (c *Config) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(c.file); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return os.WriteFile(c.file, append(data, '\n'), 0644)
}

// Snapshot is a lock-free, pointer-free view of the config for reading.
type Snapshot struct {
	Host   string  `json:"host"`
	Port   int     `json:"port"`
	Mounts []Mount `json:"mounts"`
}

func (c *Config) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Snapshot{
		Host:   c.Host,
		Port:   c.Port,
		Mounts: append([]Mount(nil), c.Mounts...),
	}
}

func (c *Config) Replace(mounts []Mount) error {
	c.mu.Lock()
	if len(mounts) > MaxMounts {
		mounts = mounts[:MaxMounts]
	}
	for i := range mounts {
		mounts[i].Path = filepath.Clean(mounts[i].Path)
	}
	c.Mounts = mounts
	c.mu.Unlock()
	return c.Save()
}

func (c *Config) MountByIndex(i int) (Mount, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if i < 0 || i >= len(c.Mounts) {
		return Mount{}, false
	}
	return c.Mounts[i], true
}
