package session

import (
	"strings"
	"testing"
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
