package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

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
	id := m.Identity()
	if id == "" {
		return mount, false
	}
	return id, true
}

// indexOf reports the current index of the named mount as a string, or "" when
// no configured mount carries that name.
func (s *StarStore) indexOf(name string) string {
	for i, m := range s.cfg.Snapshot().Mounts {
		if m.Identity() == name {
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
	// Creates the config dir if it isn't there yet. Without that, every toggle
	// 500'd on a machine whose ~/.config (or XDG_CONFIG_HOME) didn't exist:
	// reading a missing star file is a supported empty start, so nothing else
	// would have created the directory first.
	return config.WriteFileAtomic(s.path, data, 0o644)
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

// relocate rewrites the stars on mount at or below oldRel to sit at or below
// newRel instead — or drops them when newRel is "" — and persists. rename and
// delete call it, so a star follows its entry rather than pointing at a path
// that no longer exists (invisible on the page, removable only from the TUI).
func (s *StarStore) relocate(mount, oldRel, newRel string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rewrite(func(ref StarRef) (StarRef, bool) {
		if ref.Mount != mount {
			return ref, true
		}
		rest, ok := strings.CutPrefix(ref.Path, oldRel)
		if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
			return ref, true
		}
		ref.Path = newRel + rest
		return ref, newRel != ""
	})
}

// rewrite passes every star through fn, which returns the ref to keep in its
// place and whether to keep it at all, then persists if anything changed.
// Caller holds mu.
//
// It walks a copy of the set: rewriting a map while ranging over it leaves it
// to the runtime whether a re-keyed entry is visited a second time.
func (s *StarStore) rewrite(fn func(StarRef) (StarRef, bool)) error {
	before := maps.Clone(s.set)
	changed := false
	for _, ref := range s.list() {
		next, keep := fn(ref)
		if keep && next == ref {
			continue
		}
		changed = true
		delete(s.set, ref.key())
		if keep {
			s.set[next.key()] = next
		}
	}
	if !changed {
		return nil
	}
	if err := s.save(); err != nil {
		s.set = before // roll back so memory matches disk
		return err
	}
	return nil
}

// PruneMissing drops the stars whose entry no longer exists and reports how
// many it dropped. Rename and delete through the API keep the list in step
// (relocate), but a file removed any other way — a shell, another machine on
// the share, a recording that expired — left its star behind for good: nothing
// on the page shows a star without an entry, so only the TUI could remove it.
// main runs this once at startup.
//
// It removes only what it can tell is gone (see entryGone), and leaves alone a
// star whose mount is no longer configured: there is no directory to check it
// against, and the TUI lists those as "(no such mount)" to be removed by hand.
//
// The filesystem is read without mu held — a stalled network mount would
// otherwise block every star request behind it.
func (s *StarStore) PruneMissing() int {
	mounts := s.cfg.Snapshot().Mounts
	gone := map[string]bool{}
	for _, ref := range s.List() {
		i := slices.IndexFunc(mounts, func(m config.Mount) bool { return m.Identity() == ref.Mount })
		if i < 0 {
			continue
		}
		if entryGone(mounts[i].Path, ref.Path) {
			gone[ref.key()] = true
		}
	}
	if len(gone) == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	err := s.rewrite(func(ref StarRef) (StarRef, bool) {
		if !gone[ref.key()] {
			return ref, true
		}
		removed++
		log.Printf("stars: dropped %s:%s, which no longer exists", ref.Mount, ref.Path)
		return ref, false
	})
	if err != nil {
		log.Printf("stars: could not persist the clean-up: %v", err)
		return 0
	}
	return removed
}

// entryGone reports whether rel, inside the mount at root, has been deleted —
// as opposed to merely being out of reach right now. The difference matters
// because a media mount is typically a network share or a removable disk, and
// one that isn't mounted when the server starts looks exactly like one whose
// files were all deleted: an empty directory where the mountpoint is. Trusting
// that would wipe every star on the disk the first time the server came up
// before the disk did.
//
// So an entry counts as gone only when it is missing **and** the nearest
// directory above it that still exists has something else in it — which is
// what a deletion leaves behind, and not what an unmounted mountpoint looks
// like, at the mount root or anywhere below it (a mount spanning several disks).
// The price is a star under a directory that was emptied but kept: it stays
// until the directory goes too. Any other failure (permissions, I/O, a path
// that leaves the mount) keeps the star as well.
//
// Lstat, not Stat: a dangling symlink is still an entry in the listing.
func entryGone(root, rel string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	full, err := safeJoin(root, rel)
	if err != nil || full == absRoot {
		return false
	}
	if _, err := os.Lstat(full); !isMissing(err) {
		return false
	}
	// safeJoin keeps full inside absRoot, so this walk ends at absRoot at the
	// latest.
	for dir := filepath.Dir(full); ; dir = filepath.Dir(dir) {
		populated, err := hasEntries(dir)
		switch {
		case err == nil:
			return populated
		case !isMissing(err) || dir == absRoot:
			return false
		}
	}
}

// isMissing reports whether err says the path does not exist. ENOTDIR is the
// same answer in a different spelling: a component of the path is now a file.
func isMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// hasEntries reports whether dir contains anything, reading one name rather
// than the whole listing.
func hasEntries(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return len(names) > 0, nil
}

// FollowMounts carries the stars across a mount list edit that renamed a
// mount. Stars are keyed by mount identity (see StarRef), so giving a mount a
// new name used to orphan every star under it — still listed in the TUI, as
// "(no such mount)", but gone from the page. Both ways of editing mounts call
// this: the TUI's form and POST /api/config.
//
// before and after are the mount lists either side of the edit.
func (s *StarStore) FollowMounts(before, after []config.Mount) error {
	renames := renamedMounts(before, after)
	if len(renames) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rewrite(func(ref StarRef) (StarRef, bool) {
		if to, ok := renames[ref.Mount]; ok {
			ref.Mount = to
		}
		return ref, true
	})
}

// renamedMounts maps old identity → new identity for the mounts an edit
// renamed: an identity that left the list while exactly one new identity
// appeared on the same path. A mount list arrives whole, with nothing saying
// which entry was edited, so the directory is what ties the two together. A
// mount whose path changed along with its name is a different directory and is
// not followed, and neither is an ambiguous match — an orphaned star can be
// re-made, one silently attached to the wrong mount cannot be noticed.
func renamedMounts(before, after []config.Mount) map[string]string {
	had, has := map[string]bool{}, map[string]bool{}
	for _, m := range before {
		had[m.Identity()] = true
	}
	for _, m := range after {
		has[m.Identity()] = true
	}
	out := map[string]string{}
	for _, old := range before {
		if has[old.Identity()] {
			continue // still there under the same identity
		}
		fresh := map[string]bool{}
		for _, m := range after {
			if m.Path == old.Path && !had[m.Identity()] {
				fresh[m.Identity()] = true
			}
		}
		if len(fresh) == 1 {
			for to := range fresh {
				out[old.Identity()] = to
			}
		}
	}
	return out
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
	if !decodeBody(w, r, &ref) {
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
	ref.Mount = mount.Identity()
	starred, err := h.Stars.toggle(ref)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"starred": starred})
}
