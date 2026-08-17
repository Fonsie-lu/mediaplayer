package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"mediaplayer/internal/config"
)

// StarRef identifies a starred list entry. Mount is the mount's **name** and
// Path is the entry's rel_path within it.
//
// The name is the identity, not the index: mount indices renumber whenever
// mounts are reordered or one is deleted in the TUI, which used to silently
// re-point every star in the list at a different directory (or orphan it).
// Names survive that. The browser page still works in indices — it matches
// `${mount}:${path}` against its own cursor position — so the HTTP layer
// translates: an incoming mount is resolved through the config (index or name,
// see resolveMount), and outgoing refs carry both the current index and the
// name.
type StarRef struct {
	Mount string `json:"mount"`
	Path  string `json:"path"`
}

func (s StarRef) key() string { return s.Mount + ":" + s.Path }

// starWire is what the API returns: the stable name plus the index the client
// needs to match its own list. Mount is empty when no configured mount carries
// that name any more — the entry is still listed so it can be unstarred, it
// just can't be matched to anything on screen.
type starWire struct {
	Mount string `json:"mount"`
	Name  string `json:"mount_name"`
	Path  string `json:"path"`
}

// StarStore persists the set of starred entries to a JSON file. All access is
// guarded by mu; every mutation rewrites the whole file (the set is tiny).
type StarStore struct {
	path string
	cfg  *config.Config // resolves mount names ↔ indices
	mu   sync.Mutex
	set  map[string]StarRef // key() -> ref
}

// NewStarStore loads existing stars from path (a missing file is fine — starts
// empty) and returns a ready store. cfg is needed to migrate legacy
// index-keyed entries and to report current indices to the browser.
func NewStarStore(path string, cfg *config.Config) (*StarStore, error) {
	s := &StarStore{path: path, cfg: cfg, set: map[string]StarRef{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var refs []StarRef
	if len(data) > 0 {
		if err := json.Unmarshal(data, &refs); err != nil {
			return nil, err
		}
	}
	migrated := 0
	for _, r := range refs {
		if name, ok := s.migrateMount(r.Mount); ok {
			r.Mount = name
			migrated++
		}
		s.set[r.key()] = r
	}
	// Persist the migration once, so the next reorder can't re-break what we
	// just fixed. A failure here is not fatal: the in-memory set is already
	// correct and the next toggle rewrites the file anyway.
	if migrated > 0 {
		log.Printf("stars: migrated %d entr%s from mount index to mount name", migrated,
			map[bool]string{true: "y", false: "ies"}[migrated == 1])
		s.mu.Lock()
		err := s.save()
		s.mu.Unlock()
		if err != nil {
			log.Printf("stars: could not persist migration: %v", err)
		}
	}
	return s, nil
}

// mountIdentity is the stable key for a mount: its name, or its path when the
// name is empty. Two unnamed mounts would otherwise share the identity "" and
// collapse each other's stars.
func mountIdentity(m config.Mount) string {
	if m.Name != "" {
		return m.Name
	}
	return m.Path
}

// migrateMount converts a legacy numeric mount field to the mount's name,
// reporting whether it did. A number that names no current mount is left alone:
// the star is already orphaned and guessing would attach it to the wrong
// directory. A non-numeric value is already a name.
func (s *StarStore) migrateMount(mount string) (string, bool) {
	i, err := strconv.Atoi(mount)
	if err != nil {
		return mount, false
	}
	m, ok := s.cfg.MountByIndex(i)
	if !ok {
		return mount, false
	}
	id := mountIdentity(m)
	if id == "" {
		return mount, false
	}
	return id, true
}

// indexOf reports the current index of the named mount as a string, or "" when
// no configured mount carries that name.
func (s *StarStore) indexOf(name string) string {
	for i, m := range s.cfg.Snapshot().Mounts {
		if mountIdentity(m) == name {
			return strconv.Itoa(i)
		}
	}
	return ""
}

// list returns the starred refs, sorted for a stable on-disk/API ordering.
func (s *StarStore) list() []StarRef {
	out := make([]StarRef, 0, len(s.set))
	for _, r := range s.set {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mount != out[j].Mount {
			return out[i].Mount < out[j].Mount
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// save writes the current set to disk. Caller must hold mu.
func (s *StarStore) save() error {
	data, err := json.MarshalIndent(s.list(), "", "  ")
	if err != nil {
		return err
	}
	// Create the config dir if it isn't there yet — same as config.Save. Without
	// this, every toggle 500s on a machine whose ~/.config (or XDG_CONFIG_HOME)
	// doesn't exist: reading a missing star file is a supported empty start, so
	// nothing else would have created the directory first.
	if dir := filepath.Dir(s.path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(s.path, data, 0o644)
}

// toggle flips the star for ref and persists. Returns the new starred state.
func (s *StarStore) toggle(ref StarRef) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := ref.key()
	starred := true
	if _, ok := s.set[k]; ok {
		delete(s.set, k)
		starred = false
	} else {
		s.set[k] = ref
	}
	if err := s.save(); err != nil {
		// roll back the in-memory change so memory matches disk
		if starred {
			delete(s.set, k)
		} else {
			s.set[k] = ref
		}
		return false, err
	}
	return starred, nil
}

// List returns the starred refs in stable order (for the TUI / external use).
// Mount is the mount name.
func (s *StarStore) List() []StarRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list()
}

// Remove deletes ref from the store and persists. A no-op if not present.
func (s *StarStore) Remove(ref StarRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := ref.key()
	old, ok := s.set[k]
	if !ok {
		return nil
	}
	delete(s.set, k)
	if err := s.save(); err != nil {
		s.set[k] = old // roll back so memory matches disk
		return err
	}
	return nil
}

func (h *Handler) getStars(w http.ResponseWriter, r *http.Request) {
	refs := h.Stars.List()
	out := make([]starWire, 0, len(refs))
	for _, ref := range refs {
		out = append(out, starWire{
			Mount: h.Stars.indexOf(ref.Mount),
			Name:  ref.Mount,
			Path:  ref.Path,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) toggleStar(w http.ResponseWriter, r *http.Request) {
	var ref StarRef
	if !decodePost(w, r, &ref) {
		return
	}
	if ref.Path == "" {
		writeErr(w, http.StatusBadRequest, "path required")
		return
	}
	// The client sends whatever it browses by (an index); store the name.
	mount, err := resolveMount(h.Cfg, ref.Mount)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	ref.Mount = mountIdentity(mount)
	starred, err := h.Stars.toggle(ref)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"starred": starred})
}
