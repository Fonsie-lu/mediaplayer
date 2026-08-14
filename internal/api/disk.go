package api

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"mediaplayer/internal/config"
)

const procMounts = "/proc/mounts"

// diskInfo is the /api/disk payload. OK=false means "no usable number" — the
// browser hides the widget rather than rendering a misleading zero.
type diskInfo struct {
	OK         bool   `json:"ok"`
	Mountpoint string `json:"mountpoint,omitempty"`
	TotalBytes uint64 `json:"total_bytes,omitempty"`
	AvailBytes uint64 `json:"avail_bytes,omitempty"`
	// UsedBytes is total minus *unprivileged-available*, so root-reserved
	// blocks count as used — the widget reports what a normal user can't have.
	UsedBytes   uint64  `json:"used_bytes,omitempty"`
	PercentUsed float64 `json:"percent_used,omitempty"`
}

func (h *Handler) disk(w http.ResponseWriter, r *http.Request) {
	// A mount+path pair means "report on whatever filesystem the browser is
	// currently looking at". statfs resolves a directory to the filesystem
	// holding it, so a mount living on its own disk reports that disk with no
	// configuration at all. Anything unresolvable here (stale mount index,
	// traversal attempt, unreadable directory) falls through to the configured
	// disk instead of erroring — the widget's contract is a number or nothing.
	if q := r.URL.Query(); q.Get("mount") != "" {
		if info, err := currentDirUsage(h.Cfg, q.Get("mount"), q.Get("path")); err == nil {
			writeJSON(w, http.StatusOK, info)
			return
		}
	}
	spec := h.Cfg.DiskSpec()
	if spec == "" {
		writeJSON(w, http.StatusOK, diskInfo{})
		return
	}
	info, err := diskUsage(spec)
	if err != nil {
		logDiskChange(spec, err)
		writeJSON(w, http.StatusOK, diskInfo{})
		return
	}
	logDiskChange(spec, nil)
	writeJSON(w, http.StatusOK, info)
}

// diskUsage resolves the configured disk (a device node such as
// /dev/nvme0n1p3, or a plain directory) and statfs's it.
func diskUsage(spec string) (diskInfo, error) {
	mp := spec
	if strings.HasPrefix(spec, "/dev/") {
		// statfs on a device node reports the filesystem *holding the node*
		// (devtmpfs), not the filesystem on the device — so the mountpoint has
		// to come from the kernel's mount table first.
		table, err := os.ReadFile(procMounts)
		if err != nil {
			return diskInfo{}, err
		}
		resolved, _ := filepath.EvalSymlinks(spec) // /dev/mapper/*, /dev/disk/by-*
		mp, err = mountpointFor(string(table), spec, resolved)
		if err != nil {
			return diskInfo{}, fmt.Errorf("%s: %w", spec, err)
		}
	}
	return usageAt(mp, mp)
}

// currentDirUsage reports on the filesystem holding the directory the browser
// is showing. It goes through the same mount resolution and safeJoin as every
// other path-taking handler — the query names a mount-relative path, so a
// caller must not be able to point it at an arbitrary host directory and read
// back that filesystem's size.
func currentDirUsage(cfg *config.Config, mountRef, rel string) (diskInfo, error) {
	mount, err := resolveMount(cfg, mountRef)
	if err != nil {
		return diskInfo{}, err
	}
	dir, err := safeJoin(mount.Path, rel)
	if err != nil {
		return diskInfo{}, err
	}
	info, err := usageAt(dir, "")
	if err != nil {
		// statfs needs to traverse into dir, which a subdirectory the server
		// can list but not enter (a root-only vfat, say) refuses. The mount
		// root is on the same filesystem in every case but a nested mount, so
		// it's a better answer than none.
		dir = mount.Path
		if info, err = usageAt(dir, ""); err != nil {
			return diskInfo{}, err
		}
	}
	// statfs answered the size question; /proc/mounts only supplies the label.
	// Resolve first: the filesystem statfs reported is the one holding the
	// *link target*, so an unresolved path would name the wrong mountpoint.
	if table, err := os.ReadFile(procMounts); err == nil {
		info.Mountpoint = mountpointOf(string(table), resolveExisting(dir))
	}
	return info, nil
}

// usageAt statfs's path and reports it under label (the mountpoint the caller
// wants shown, which is not always the path that was measured).
func usageAt(path, label string) (diskInfo, error) {
	total, avail, err := statfsUsage(path)
	if err != nil {
		return diskInfo{}, err
	}
	if total == 0 {
		return diskInfo{}, fmt.Errorf("%s: zero-sized filesystem", path)
	}
	used := total - avail // statfs guarantees Bavail <= Blocks
	return diskInfo{
		OK:          true,
		Mountpoint:  label,
		TotalBytes:  total,
		AvailBytes:  avail,
		UsedBytes:   used,
		PercentUsed: math.Round(float64(used)/float64(total)*1000) / 10,
	}, nil
}

// mountpointOf returns the longest mountpoint in a /proc/mounts-formatted
// table that contains path — the same rule the kernel uses, so a subvolume or
// bind mount nested inside another filesystem wins over its parent. Purely
// cosmetic: an empty result just leaves the widget's tooltip without a name.
func mountpointOf(table, path string) string {
	best := ""
	sc := bufio.NewScanner(strings.NewReader(table))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		if mp := unescapeMountField(f[1]); contains(mp, path) && len(mp) > len(best) {
			best = mp
		}
	}
	return best
}

// contains is within() with "/" handled: paths.go's version appends a
// separator to the root, which "/" already ends with, so it would never match
// the root mountpoint — and "/" is the answer whenever nothing more specific
// is. Kept here rather than folded into within(), which is a path-safety check
// and not the place to relax a prefix rule.
func contains(dir, path string) bool {
	return dir == "/" || within(dir, path)
}

// mountpointFor scans a /proc/mounts-formatted table for any of cands (the
// configured device plus its symlink-resolved form) and returns the shortest
// matching mountpoint: one device can appear several times (bind mounts, btrfs
// subvolumes) and the shortest path is the whole-filesystem one.
func mountpointFor(table string, cands ...string) (string, error) {
	want := make(map[string]bool, len(cands))
	for _, c := range cands {
		if c != "" {
			want[c] = true
		}
	}
	best := ""
	sc := bufio.NewScanner(strings.NewReader(table))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || !want[unescapeMountField(f[0])] {
			continue
		}
		if mp := unescapeMountField(f[1]); best == "" || len(mp) < len(best) {
			best = mp
		}
	}
	if best == "" {
		return "", errors.New("device is not mounted")
	}
	return best, nil
}

// unescapeMountField undoes the octal escaping /proc/mounts applies to spaces
// (\040), tabs, newlines and backslashes in device and mountpoint fields.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// The widget polls, so log only on transitions — otherwise an unmounted disk
// would fill the TUI's Logs tab with one identical line per minute.
var diskLog struct {
	mu   sync.Mutex
	last string
}

func logDiskChange(spec string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	diskLog.mu.Lock()
	defer diskLog.mu.Unlock()
	if msg == diskLog.last {
		return
	}
	diskLog.last = msg
	if err != nil {
		log.Printf("disk %s unavailable: %v", spec, err)
	} else {
		log.Printf("disk %s available again", spec)
	}
}
