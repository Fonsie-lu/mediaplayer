package transcode

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// keyframeCache memoizes keyframe scans the same way probeCache does — the
// scan demuxes every packet header, which can take seconds on large files
// over network mounts, and the result is tiny (one float per GOP).
var keyframeCache = struct {
	sync.Mutex
	m map[string][]float64
}{m: map[string][]float64{}}

const keyframeCacheMax = 128

// KeyframeTimes returns the sorted PTS (seconds) of every video keyframe.
// Used by remux mode, where HLS segments can only split on existing
// keyframes, so the playlist must be built from real keyframe positions
// instead of a fixed segment-duration grid.
func KeyframeTimes(path string) ([]float64, error) {
	var key string
	if st, err := os.Stat(path); err == nil {
		key = path + "|" + strconv.FormatInt(st.Size(), 10) + "|" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
		keyframeCache.Lock()
		cached := keyframeCache.m[key]
		keyframeCache.Unlock()
		if cached != nil {
			return cached, nil
		}
	}
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "packet=pts_time,flags",
		"-of", "csv=p=0",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("keyframe scan: %w", err)
	}
	times := parseKeyframeCSV(string(out))
	if key != "" {
		keyframeCache.Lock()
		if len(keyframeCache.m) >= keyframeCacheMax {
			keyframeCache.m = map[string][]float64{}
		}
		keyframeCache.m[key] = times
		keyframeCache.Unlock()
	}
	return times, nil
}

// parseKeyframeCSV extracts keyframe PTS from `pts_time,flags` CSV lines,
// e.g. "12.345000,K__". Packets without a PTS ("N/A") are skipped.
func parseKeyframeCSV(out string) []float64 {
	var times []float64
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		ptsStr, flags, ok := strings.Cut(line, ",")
		if !ok || !strings.Contains(flags, "K") {
			continue
		}
		pts, err := strconv.ParseFloat(ptsStr, 64)
		if err != nil || pts < 0 {
			continue
		}
		times = append(times, pts)
	}
	sort.Float64s(times)
	return times
}

// BuildBoundaries turns keyframe times into segment boundaries: greedily,
// a new segment starts at the first keyframe at least `target` seconds
// after the previous boundary — the same rule ffmpeg's HLS muxer applies
// when stream-copying with -hls_time, so the synthetic playlist and the
// files ffmpeg actually emits agree. The result always starts at 0 and
// ends at `duration`, so it has NumSegments+1 entries.
func BuildBoundaries(keyframes []float64, duration, target float64) []float64 {
	bounds := []float64{0}
	last := 0.0
	for _, kf := range keyframes {
		if kf-last >= target && kf < duration {
			bounds = append(bounds, kf)
			last = kf
		}
	}
	// Avoid a stub segment at the tail: fold a sub-second remainder into the
	// previous segment.
	if n := len(bounds); n > 1 && duration-bounds[n-1] < 1.0 {
		bounds = bounds[:n-1]
	}
	return append(bounds, duration)
}

// MaxGap returns the longest segment duration in a boundary list.
func MaxGap(bounds []float64) float64 {
	max := 0.0
	for i := 1; i < len(bounds); i++ {
		if d := bounds[i] - bounds[i-1]; d > max {
			max = d
		}
	}
	return max
}
