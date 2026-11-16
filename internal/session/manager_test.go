package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediaplayer/internal/transcode"
)

func TestNumSegments(t *testing.T) {
	cases := []struct {
		dur  float64
		want int
	}{
		{0, 0},
		{3.9, 1},
		{4.0, 1},
		{4.1, 2},
		{64, 16},
		{3600, 900},
	}
	for _, c := range cases {
		s := &Session{Duration: c.dur}
		if got := s.NumSegments(); got != c.want {
			t.Errorf("NumSegments(dur=%.1f) = %d, want %d", c.dur, got, c.want)
		}
	}
}

func TestPlaylistText(t *testing.T) {
	s := &Session{Duration: 10} // 2 full segments + 2s remainder
	pl := s.PlaylistText()

	for _, want := range []string{
		"#EXTM3U",
		"#EXT-X-PLAYLIST-TYPE:VOD",
		"#EXT-X-ENDLIST",
		"seg_00000.ts",
		"seg_00002.ts",
		"#EXTINF:2.000,",
	} {
		if !strings.Contains(pl, want) {
			t.Errorf("playlist missing %q:\n%s", want, pl)
		}
	}
	if strings.Contains(pl, "seg_00003.ts") {
		t.Error("playlist has segment past the end")
	}
}

func TestBoundariesSession(t *testing.T) {
	s := &Session{
		Duration:   20,
		Boundaries: []float64{0, 9.5, 17.2, 20},
	}
	if got := s.NumSegments(); got != 3 {
		t.Fatalf("NumSegments = %d, want 3", got)
	}
	pl := s.PlaylistText()
	for _, want := range []string{
		"#EXT-X-TARGETDURATION:10", // longest segment is 9.5s
		"#EXTINF:9.500,",
		"#EXTINF:7.700,",
		"#EXTINF:2.800,",
		"seg_00002.ts",
	} {
		if !strings.Contains(pl, want) {
			t.Errorf("playlist missing %q:\n%s", want, pl)
		}
	}
	if strings.Contains(pl, "seg_00003.ts") {
		t.Error("playlist has segment past the end")
	}
	if got := s.segStart(3); got != 20 {
		t.Errorf("segStart(N) = %v, want duration", got)
	}
}

// batchSpecFor is the boundary between session bookkeeping and the ffmpeg
// command line, so its clamping and split-time derivation are worth pinning:
// an off-by-one here shifts every segment of a batch.
func TestBatchSpecForMidVideo(t *testing.T) {
	s := &Session{Duration: 3600, Input: "/m/f.mkv", Dir: "/tmp/x", MaxHeight: 720, AudioIdx: 1}
	spec := s.batchSpecFor(10)

	if spec.StartSeg != 10 || spec.Count != BatchSize {
		t.Errorf("StartSeg/Count = %d/%d, want 10/%d", spec.StartSeg, spec.Count, BatchSize)
	}
	if spec.StartSec != 40 {
		t.Errorf("StartSec = %v, want 40 (segment 10 on the uniform grid)", spec.StartSec)
	}
	if want := BatchSize * SegDuration; spec.DurSec != want {
		t.Errorf("DurSec = %v, want %v", spec.DurSec, want)
	}
	if spec.SplitTimes != nil {
		t.Errorf("SplitTimes = %v, want nil in encode mode (uniform grid)", spec.SplitTimes)
	}
	// Session fields must reach ffmpeg unchanged — a dropped one silently
	// serves the wrong track or the wrong resolution.
	if spec.Input != s.Input || spec.Dir != s.Dir || spec.MaxHeight != 720 || spec.AudioIdx != 1 {
		t.Errorf("spec lost session fields: %+v", spec)
	}
}

// The last batch must stop at the end of the video, not run past it.
func TestBatchSpecForClampsAtTail(t *testing.T) {
	s := &Session{Duration: 50} // 13 segments: 12 full + a 2s tail
	n := s.NumSegments() - 3
	spec := s.batchSpecFor(n)

	if spec.Count != 3 {
		t.Errorf("Count = %d, want 3 (segments remaining)", spec.Count)
	}
	if got := spec.StartSec + spec.DurSec; got != s.Duration {
		t.Errorf("batch ends at %v, want the video duration %v", got, s.Duration)
	}
}

func TestBatchSpecForRemuxSplitTimes(t *testing.T) {
	// 5 segments on real keyframe boundaries.
	s := &Session{
		Duration:   40,
		Boundaries: []float64{0, 8.5, 17.25, 24, 33.75, 40},
		CopyVideo:  true, CopyAudio: true,
	}
	spec := s.batchSpecFor(1)

	if spec.Count != 4 {
		t.Fatalf("Count = %d, want the 4 segments from index 1", spec.Count)
	}
	if spec.StartSec != 8.5 {
		t.Errorf("StartSec = %v, want the keyframe at 8.5", spec.StartSec)
	}
	if got := spec.StartSec + spec.DurSec; got != 40 {
		t.Errorf("batch ends at %v, want 40", got)
	}
	// Interior boundaries only: the batch's own start is not a split, and the
	// end needs none either.
	want := []float64{17.25, 24, 33.75}
	if len(spec.SplitTimes) != len(want) {
		t.Fatalf("SplitTimes = %v, want %v", spec.SplitTimes, want)
	}
	for i := range want {
		if spec.SplitTimes[i] != want[i] {
			t.Errorf("SplitTimes[%d] = %v, want %v", i, spec.SplitTimes[i], want[i])
		}
	}
	if !spec.CopyVideo || !spec.CopyAudio {
		t.Error("remux flags lost on the way to the spec")
	}
}

// A single-segment batch has no interior boundaries at all.
func TestBatchSpecForSingleRemuxSegment(t *testing.T) {
	s := &Session{Duration: 20, Boundaries: []float64{0, 9.5, 17.2, 20}, CopyVideo: true}
	spec := s.batchSpecFor(2)
	if spec.Count != 1 {
		t.Fatalf("Count = %d, want 1", spec.Count)
	}
	if spec.SplitTimes != nil {
		t.Errorf("SplitTimes = %v, want none for a single segment", spec.SplitTimes)
	}
}

func touchSeg(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.WriteFile(segPath(dir, n), []byte("ts"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Encode batches rename finished segments into place, so presence is proof;
// remux batches write in place, so only a successor (or exit) is.
func TestSegmentCompleteByMuxer(t *testing.T) {
	dir := t.TempDir()
	touchSeg(t, dir, 4)
	touchSeg(t, dir, 5)

	encode := &transcode.Batch{StartSeg: 4, Count: 4}
	if !segmentComplete(dir, 4, encode) || !segmentComplete(dir, 5, encode) {
		t.Error("encode batch: a present segment should be complete")
	}
	remux := &transcode.Batch{StartSeg: 4, Count: 4, Sequential: true}
	if !segmentComplete(dir, 4, remux) {
		t.Error("remux batch: segment with a successor on disk should be complete")
	}
	if segmentComplete(dir, 5, remux) {
		t.Error("remux batch: the newest file may still be growing")
	}
	if segmentComplete(dir, 6, encode) {
		t.Error("a missing segment is never complete")
	}
	if !segmentComplete(dir, 5, nil) {
		t.Error("with no batch writing, a present file is complete")
	}
}

// A batch being stopped vouches for nothing in its range until it has exited:
// SIGTERM makes the hls muxer finalize its half-written segment under the real
// name. That holds when it is only the retiring batch, too — the window where
// the session has no current batch at all is exactly when it matters.
func TestSegmentCompleteDistrustsStoppingBatch(t *testing.T) {
	dir := t.TempDir()
	touchSeg(t, dir, 4)
	stopping := &transcode.Batch{StartSeg: 0, Count: 16}
	stopping.Stop() // no process: only marks it stopping

	if segmentComplete(dir, 4, stopping) {
		t.Error("a stopping batch's segment was trusted")
	}
	if segmentComplete(dir, 4, nil, stopping) {
		t.Error("a retiring batch's segment was trusted while no batch is current")
	}
	if !segmentComplete(dir, 4, nil, &transcode.Batch{StartSeg: 20, Count: 16}) {
		t.Error("a batch that doesn't cover the segment must not veto it")
	}
}

// The janitor must tell a dead run's leftovers from the scratch dirs of
// another instance that is still serving: it used to delete every matching
// dir, so starting a second server killed the first one's streams.
func TestCleanStaleTempDirsSparesLiveInstances(t *testing.T) {
	root := t.TempDir()
	const self, live = 100, 200 // 300 is a pid nobody has
	dirs := map[string]bool{    // name -> survives
		SessTempPrefix + "200-111":  true,  // another instance, running
		ThumbTempPrefix + "200-222": true,  // its preview scratch too
		SessTempPrefix + "300-333":  false, // its owner is gone
		SheetTempPrefix + "100-444": false, // ours, from before a re-exec
		SessTempPrefix + "555":      false, // legacy name, no pid
		SessTempPrefix + "x-666":    false, // unparsable pid
		SessTempPrefix + "200":      false, // no separator, so no pid field
		"mediaplayer-previews":      true,  // the stable cache
		"unrelated-200-777":         true,
	}
	for name := range dirs {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A plain file under a matching name is never touched.
	file := filepath.Join(root, SessTempPrefix+"300-file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cleanStaleTempDirs(root, self, func(pid int) bool { return pid == live || pid == self })

	for name, want := range dirs {
		_, err := os.Stat(filepath.Join(root, name))
		if got := err == nil; got != want {
			t.Errorf("%s: survived=%t, want %t", name, got, want)
		}
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("plain file removed: %v", err)
	}
}

func TestMkdirTempCarriesPid(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir, err := MkdirTemp(SessTempPrefix)
	if err != nil {
		t.Fatal(err)
	}
	pid, ours := tempDirOwner(filepath.Base(dir))
	if !ours || pid != os.Getpid() {
		t.Errorf("tempDirOwner(%q) = %d, %t; want this pid", filepath.Base(dir), pid, ours)
	}
}
