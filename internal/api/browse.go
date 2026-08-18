package api

import (
	"cmp"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type FileEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mtime   int64  `json:"mtime"` // unix seconds
	Ctime   int64  `json:"ctime"`
	Kind    string `json:"kind"` // video | folder | other
	RelPath string `json:"rel_path"`

	lowerName string // sort key, computed once in sortEntries
}

var videoExts = map[string]bool{
	".mp4": true, ".m4v": true, ".mkv": true, ".webm": true, ".ts": true,
	".m2ts": true, ".mts": true, ".avi": true, ".mov": true, ".wmv": true,
	".flv": true, ".mpg": true, ".mpeg": true, ".3gp": true, ".ogv": true,
	".vob": true, ".rm": true, ".rmvb": true,
}

func classify(name string, isDir bool) string {
	if isDir {
		return "folder"
	}
	ext := strings.ToLower(filepath.Ext(name))
	if videoExts[ext] {
		return "video"
	}
	return "other"
}

func (h *Handler) getMounts(w http.ResponseWriter, r *http.Request) {
	snap := h.Cfg.Snapshot()
	out := make([]map[string]any, len(snap.Mounts))
	for i, m := range snap.Mounts {
		out[i] = map[string]any{"index": i, "name": m.Name, "path": m.Path}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) browse(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "ctime_desc"
	}
	mount, full, ok := h.queryTarget(w, r)
	if !ok {
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		relChild := filepath.Join(rel, e.Name())
		out = append(out, FileEntry{
			Name:    e.Name(),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			Mtime:   info.ModTime().Unix(),
			Ctime:   ctimeOf(info),
			Kind:    classify(e.Name(), e.IsDir()),
			RelPath: relChild,
		})
	}
	sortEntries(out, sortBy)
	writeJSON(w, http.StatusOK, map[string]any{
		"mount":   mount.Name,
		"path":    rel,
		"entries": out,
	})
}

func sortEntries(e []FileEntry, by string) {
	if by == "name_asc" || by == "name_desc" {
		for i := range e {
			e[i].lowerName = strings.ToLower(e[i].Name)
		}
	}
	// Folders first always, whatever the key — then the key itself, as a
	// three-way compare so each case reads in the direction it sorts.
	slices.SortStableFunc(e, func(a, b FileEntry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		switch by {
		case "name_asc":
			return cmp.Compare(a.lowerName, b.lowerName)
		case "name_desc":
			return cmp.Compare(b.lowerName, a.lowerName)
		case "size_asc":
			return cmp.Compare(a.Size, b.Size)
		case "size_desc":
			return cmp.Compare(b.Size, a.Size)
		case "ctime_asc":
			return cmp.Compare(a.Ctime, b.Ctime)
		default: // ctime_desc, and anything unrecognized
			return cmp.Compare(b.Ctime, a.Ctime)
		}
	})
}
