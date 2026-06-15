package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
	// folders first always
	sort.SliceStable(e, func(i, j int) bool {
		if e[i].IsDir != e[j].IsDir {
			return e[i].IsDir
		}
		switch by {
		case "name_asc":
			return e[i].lowerName < e[j].lowerName
		case "name_desc":
			return e[i].lowerName > e[j].lowerName
		case "size_asc":
			return e[i].Size < e[j].Size
		case "size_desc":
			return e[i].Size > e[j].Size
		case "ctime_asc":
			return e[i].Ctime < e[j].Ctime
		case "ctime_desc":
			fallthrough
		default:
			return e[i].Ctime > e[j].Ctime
		}
	})
}
