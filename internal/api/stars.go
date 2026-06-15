package api

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"sync"
)

// StarRef identifies a starred list entry. Mount is the mount index (as sent by
// the client) and Path is the entry's rel_path within that mount.
type StarRef struct {
	Mount string `json:"mount"`
	Path  string `json:"path"`
}

func (s StarRef) key() string { return s.Mount + ":" + s.Path }

// StarStore persists the set of starred entries to a JSON file. All access is
// guarded by mu; every mutation rewrites the whole file (the set is tiny).
type StarStore struct {
	path string
	mu   sync.Mutex
	set  map[string]StarRef // key() -> ref
}

// NewStarStore loads existing stars from path (a missing file is fine — starts
// empty) and returns a ready store.
func NewStarStore(path string) (*StarStore, error) {
	s := &StarStore{path: path, set: map[string]StarRef{}}
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
	for _, r := range refs {
		s.set[r.key()] = r
	}
	return s, nil
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

func (h *Handler) getStars(w http.ResponseWriter, r *http.Request) {
	h.Stars.mu.Lock()
	out := h.Stars.list()
	h.Stars.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) toggleStar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var ref StarRef
	if err := json.NewDecoder(r.Body).Decode(&ref); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if ref.Path == "" {
		writeErr(w, http.StatusBadRequest, "path required")
		return
	}
	starred, err := h.Stars.toggle(ref)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"starred": starred})
}
