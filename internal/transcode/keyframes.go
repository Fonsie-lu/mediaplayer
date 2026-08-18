package transcode

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// keyframeCache memoizes keyframe scans the same way probeCache does — the
// scan demuxes every packet header, which can take seconds on large files
// over network mounts, and the result is tiny (one float per GOP).
var keyframeCache = newLRU[[]float64](keyframeCacheMax)

const keyframeCacheMax = 128

// KeyframeTimes returns the sorted PTS (seconds) of every video keyframe.
// Used by remux mode, where HLS segments can only split on existing
// keyframes, so the playlist must be built from real keyframe positions
// instead of a fixed segment-duration grid.
func KeyframeTimes(path string) ([]float64, error) {
	key := statKey(path)
	if cached, ok := keyframeCache.get(key); ok {
		return cached, nil
	}
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "packet=pts_time,flags",
		"-of", "csv=p=0",
		path,
	)
	// Streamed rather than buffered: this is one CSV line per *packet header*,
	// several MB for a feature-length file, and the scan itself takes seconds
	// on a network mount. Reading it line by line keeps peak memory at one
	// line and parses while ffprobe is still demuxing.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("keyframe scan: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("keyframe scan: %w", err)
	}
	times := scanKeyframes(stdout)
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("keyframe scan: %w", err)
	}
	keyframeCache.put(key, times)
	return times, nil
}

// scanKeyframes extracts keyframe PTS from a stream of `pts_time,flags` CSV
// lines, e.g. "12.345000,K__". Packets without a PTS ("N/A") are skipped.
func scanKeyframes(r io.Reader) []float64 {
	var times []float64
	sc := bufio.NewScanner(r)
	// A CSV line is short, but a corrupt stream shouldn't kill the scan with
	// bufio.Scanner's default 64KB token limit.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
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
	slices.Sort(times)
	return times
}

// parseKeyframeCSV is scanKeyframes over a whole string — the shape the tests
// use, and the reason the parse stayed separate from the ffprobe plumbing.
func parseKeyframeCSV(out string) []float64 {
	return scanKeyframes(strings.NewReader(out))
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
	worst := 0.0
	for i := 1; i < len(bounds); i++ {
		worst = max(worst, bounds[i]-bounds[i-1])
	}
	return worst
}
