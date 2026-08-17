package transcode

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ---------- helpers ----------

// argVal returns the value following the first occurrence of flag.
func argVal(t *testing.T, args []string, flag string) string {
	t.Helper()
	i := slices.Index(args, flag)
	if i < 0 || i == len(args)-1 {
		t.Fatalf("%s not present (args: %s)", flag, strings.Join(args, " "))
	}
	return args[i+1]
}

// argVals returns every value following an occurrence of flag (-map appears
// more than once).
func argVals(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i < len(args)-1 {
			out = append(out, args[i+1])
		}
	}
	return out
}

func has(args []string, flag string) bool { return slices.Contains(args, flag) }

func argFloat(t *testing.T, args []string, flag string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(argVal(t, args, flag), 64)
	if err != nil {
		t.Fatalf("%s = %q, not a number", flag, argVal(t, args, flag))
	}
	return v
}

func closeTo(a, b float64) bool { return a-b < 0.001 && b-a < 0.001 }

// ---------- shared invariants ----------

// Both muxers must be told not to shift the timeline, or every segment's
// content starts 1.4s after its playlist position and hls.js request-storms on
// each seek. And no -re: bounded batch length is the rate limit, so throttling
// to realtime would just make batches late.
func TestBatchArgsTimelineInvariants(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec BatchSpec
	}{
		{"encode", BatchSpec{Input: "in.mkv", Dir: "/d", StartSeg: 5, Count: 4, StartSec: 20, DurSec: 16, SegDur: 4}},
		{"remux", BatchSpec{Input: "in.mkv", Dir: "/d", StartSeg: 5, Count: 4, StartSec: 20, DurSec: 16, SegDur: 4,
			CopyVideo: true, SplitTimes: []float64{24, 28, 32}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := batchArgs(tc.spec, planBatch1(tc.spec))
			if got := argVal(t, args, "-muxdelay"); got != "0" {
				t.Errorf("-muxdelay = %q, want 0", got)
			}
			if got := argVal(t, args, "-avoid_negative_ts"); got != "make_non_negative" {
				t.Errorf("-avoid_negative_ts = %q, want make_non_negative", got)
			}
			if has(args, "-re") {
				t.Error("-re present: batch length is the rate limit, not realtime throttling")
			}
			// -ss must precede -i or it becomes a slow output-side seek.
			if slices.Index(args, "-ss") > slices.Index(args, "-i") {
				t.Error("-ss must come before -i (input-side seek)")
			}
			if got := argVals(args, "-map"); len(got) != 2 {
				t.Errorf("-map count = %d, want 2 (one video, one audio)", len(got))
			}
		})
	}
}

func TestBatchArgsAudioTrackSelection(t *testing.T) {
	spec := BatchSpec{Input: "in.mkv", Dir: "/d", Count: 2, DurSec: 8, SegDur: 4, AudioIdx: 2}
	args := batchArgs(spec, planBatch1(spec))
	maps := argVals(args, "-map")
	// The index is audio-stream-relative, and both maps are optional (?) so a
	// file with no audio still produces video.
	if want := "0:a:2?"; !slices.Contains(maps, want) {
		t.Errorf("-map %s missing, got %v", want, maps)
	}
	if want := "0:v:0?"; !slices.Contains(maps, want) {
		t.Errorf("-map %s missing, got %v", want, maps)
	}
}

// ---------- remux mode ----------

func TestBatchArgsRemuxUsesSegmentMuxer(t *testing.T) {
	spec := BatchSpec{
		Input: "in.mkv", Dir: "/d", StartSeg: 20, Count: 4,
		StartSec: 80, DurSec: 16, SegDur: 4,
		CopyVideo: true, CopyAudio: true,
		SplitTimes: []float64{84, 88, 92},
	}
	// Landing exactly on the requested second: no slop.
	args := batchArgs(spec, batchPlan{seekAt: 80, anchor: 80})

	if got := argVal(t, args, "-c:v"); got != "copy" {
		t.Errorf("-c:v = %q, want copy — remux must be bit-identical", got)
	}
	if got := argVal(t, args, "-c:a"); got != "copy" {
		t.Errorf("-c:a = %q, want copy for a copyable codec", got)
	}
	// The segment muxer, not hls: it is the one that accepts explicit split
	// times, and count-based splitting would shift every segment.
	if got := argVal(t, args, "-f"); got != "segment" {
		t.Errorf("-f = %q, want segment", got)
	}
	if has(args, "-hls_time") || has(args, "-hls_flags") {
		t.Error("hls muxer flags leaked into a remux batch")
	}
	if got := argVal(t, args, "-segment_start_number"); got != "20" {
		t.Errorf("-segment_start_number = %q, want 20", got)
	}
	if got := argVal(t, args, "-segment_times"); got != "4.000,8.000,12.000" {
		t.Errorf("-segment_times = %q, want split times relative to the anchor", got)
	}
	// Remux undoes the -ss rebase exactly, so PTS stay source-true.
	if got := argFloat(t, args, "-output_ts_offset"); !closeTo(got, 80) {
		t.Errorf("-output_ts_offset = %v, want seekAt (80)", got)
	}
	if got := argFloat(t, args, "-t"); !closeTo(got, 16) {
		t.Errorf("-t = %v, want DurSec (16) with no landing slop", got)
	}
}

// A copy-mode seek lands on whatever keyframe the container's index points at,
// which is often *before* the target. Those packets can't be trimmed away, so
// the batch keeps them: -t stretches to still reach the intended end, and the
// split times are measured from where the muxer actually starts counting.
func TestBatchArgsRemuxEarlyLanding(t *testing.T) {
	spec := BatchSpec{
		Input: "in.mkv", Dir: "/d", StartSeg: 20, Count: 4,
		StartSec: 80, DurSec: 16, SegDur: 4,
		CopyVideo: true, SplitTimes: []float64{84, 88, 92},
	}
	args := batchArgs(spec, batchPlan{seekAt: 78, anchor: 78.5}) // landed 1.5s early

	if got := argFloat(t, args, "-t"); !closeTo(got, 17.5) {
		t.Errorf("-t = %v, want DurSec + landing slop (17.5)", got)
	}
	if got := argVal(t, args, "-segment_times"); got != "5.500,9.500,13.500" {
		t.Errorf("-segment_times = %q, want times relative to anchor 78.5", got)
	}
	if got := argFloat(t, args, "-output_ts_offset"); !closeTo(got, 78) {
		t.Errorf("-output_ts_offset = %v, want seekAt (78) so PTS stay source-true", got)
	}
}

// If the probe failed or landed absurdly late (at or past the first split), the
// split times must fall back to StartSec rather than going negative and
// collapsing the batch's first segments.
func TestBatchArgsRemuxAnchorFallback(t *testing.T) {
	spec := BatchSpec{
		Input: "in.mkv", Dir: "/d", StartSeg: 20, Count: 4,
		StartSec: 80, DurSec: 16, SegDur: 4,
		CopyVideo: true, SplitTimes: []float64{84, 88, 92},
	}
	args := batchArgs(spec, batchPlan{seekAt: 80, anchor: 90}) // past the first split

	got := argVal(t, args, "-segment_times")
	if got != "4.000,8.000,12.000" {
		t.Errorf("-segment_times = %q, want the StartSec-relative fallback", got)
	}
	for _, part := range strings.Split(got, ",") {
		if strings.HasPrefix(part, "-") {
			t.Errorf("-segment_times contains a negative value: %q", got)
		}
	}
}

// A batch producing one segment has no internal boundaries; time-based
// splitting must be disabled rather than left at its default.
func TestBatchArgsRemuxSingleSegment(t *testing.T) {
	spec := BatchSpec{
		Input: "in.mkv", Dir: "/d", StartSeg: 99, Count: 1,
		StartSec: 396, DurSec: 4, SegDur: 4, CopyVideo: true,
	}
	args := batchArgs(spec, batchPlan{seekAt: 396, anchor: 396})
	if has(args, "-segment_times") {
		t.Error("-segment_times set for a single-segment batch")
	}
	if got := argVal(t, args, "-segment_time"); got != "999999" {
		t.Errorf("-segment_time = %q, want splitting effectively disabled", got)
	}
}

// ---------- encode mode ----------

func TestBatchArgsEncodeUsesHLSMuxer(t *testing.T) {
	spec := BatchSpec{
		Input: "in.mkv", Dir: "/d", StartSeg: 5, Count: 4,
		StartSec: 20, DurSec: 16, SegDur: 4,
	}
	args := batchArgs(spec, batchPlan{seekAt: 20, anchor: 20})

	if got := argVal(t, args, "-c:v"); got != "libx264" {
		t.Errorf("-c:v = %q, want libx264", got)
	}
	if got := argVal(t, args, "-f"); got != "hls" {
		t.Errorf("-f = %q, want hls", got)
	}
	// temp_file is what makes a segment appear only once fully written, so the
	// session's wait poll can treat existence as completeness.
	if got := argVal(t, args, "-hls_flags"); !strings.Contains(got, "temp_file") {
		t.Errorf("-hls_flags = %q, want temp_file", got)
	}
	// Every segment must open with an IDR or it can't be decoded independently.
	if got := argVal(t, args, "-force_key_frames"); got != "expr:gte(t,n_forced*4.000)" {
		t.Errorf("-force_key_frames = %q, want an expr on SegDur", got)
	}
	if got := argVal(t, args, "-start_number"); got != "5" {
		t.Errorf("-start_number = %q, want 5", got)
	}
	if has(args, "-segment_times") || has(args, "-segment_start_number") {
		t.Error("segment-muxer flags leaked into an encode batch")
	}
	// Post-trim the timeline is 0 at StartSec, so that is what gets added back.
	if got := argFloat(t, args, "-output_ts_offset"); !closeTo(got, 20) {
		t.Errorf("-output_ts_offset = %v, want StartSec (20)", got)
	}
	if got := argFloat(t, args, "-t"); !closeTo(got, 16) {
		t.Errorf("-t = %v, want DurSec exactly (encode trims first)", got)
	}
}

func TestBatchArgsEncodeScale(t *testing.T) {
	spec := BatchSpec{Input: "in.mkv", Dir: "/d", Count: 4, DurSec: 16, SegDur: 4, MaxHeight: 720}
	args := batchArgs(spec, batchPlan{})
	vf := argVal(t, args, "-vf")
	if !strings.Contains(vf, "min(720,ih)") {
		t.Errorf("-vf = %q, want a height cap at 720", vf)
	}
	// Only ever scale down: a 480p source must not be upscaled to the cap.
	if !strings.Contains(vf, "-2:") {
		t.Errorf("-vf = %q, want width derived from the source (-2)", vf)
	}
}

func TestBatchArgsEncodeNoScaleAtSourceQuality(t *testing.T) {
	spec := BatchSpec{Input: "in.mkv", Dir: "/d", Count: 4, DurSec: 16, SegDur: 4, MaxHeight: 0}
	args := batchArgs(spec, batchPlan{})
	if has(args, "-vf") {
		t.Errorf("-vf present with no height cap and no backoff: %v", args)
	}
}

// The backoff path is the subtle one: decoding starts early, so the output has
// to be trimmed back to StartSec and the timebase re-based, *before* any
// scaling, and the same has to happen to audio — which is why a backoff forces
// an audio re-encode even for an otherwise copyable codec.
func TestBatchArgsEncodeBackoffTrim(t *testing.T) {
	spec := BatchSpec{
		Input: "in.mkv", Dir: "/d", StartSeg: 5, Count: 4,
		StartSec: 20, DurSec: 16, SegDur: 4,
		MaxHeight: 720, CopyAudio: true,
	}
	args := batchArgs(spec, batchPlan{seekAt: 17.5, anchor: 20, backoff: 2.5})

	vf := argVal(t, args, "-vf")
	trimAt := strings.Index(vf, "trim=start=2.500")
	setptsAt := strings.Index(vf, "setpts=PTS-2.500/TB")
	scaleAt := strings.Index(vf, "scale=")
	if trimAt < 0 || setptsAt < 0 {
		t.Fatalf("-vf = %q, want trim + setpts for the 2.5s backoff", vf)
	}
	if !(trimAt < setptsAt && setptsAt < scaleAt) {
		t.Errorf("-vf = %q, want trim,setpts before scale", vf)
	}
	// A filter can't touch copied audio, so the copy must be abandoned.
	if got := argVal(t, args, "-c:a"); got != "aac" {
		t.Errorf("-c:a = %q, want aac: a backoff forces an audio re-encode", got)
	}
	af := argVal(t, args, "-af")
	if !strings.Contains(af, "atrim=start=2.500") || !strings.Contains(af, "asetpts=PTS-2.500/TB") {
		t.Errorf("-af = %q, want the audio mirror of the video trim", af)
	}
	// Downstream args see the rebased 0-at-StartSec timeline, same as the
	// no-backoff path.
	if got := argFloat(t, args, "-output_ts_offset"); !closeTo(got, 20) {
		t.Errorf("-output_ts_offset = %v, want StartSec (20) even with a backoff", got)
	}
	if got := argFloat(t, args, "-t"); !closeTo(got, 16) {
		t.Errorf("-t = %v, want DurSec (16) even with a backoff", got)
	}
}

// Remux never re-encodes audio for a backoff, because it never has one: copied
// video keeps its early packets instead of trimming them.
func TestBatchArgsRemuxCopyableAudioStaysCopied(t *testing.T) {
	spec := BatchSpec{
		Input: "in.mkv", Dir: "/d", Count: 2, StartSec: 40, DurSec: 8, SegDur: 4,
		CopyVideo: true, CopyAudio: true, SplitTimes: []float64{44},
	}
	args := batchArgs(spec, batchPlan{seekAt: 38, anchor: 39})
	if got := argVal(t, args, "-c:a"); got != "copy" {
		t.Errorf("-c:a = %q, want copy", got)
	}
	if has(args, "-af") || has(args, "-vf") {
		t.Error("filters present in a remux batch: copied streams can't be filtered")
	}
}

func TestBatchArgsIncompatibleAudioReencoded(t *testing.T) {
	// ac3/dts and friends: video still copies, audio must not.
	spec := BatchSpec{
		Input: "in.mkv", Dir: "/d", Count: 2, StartSec: 40, DurSec: 8, SegDur: 4,
		CopyVideo: true, CopyAudio: false, SplitTimes: []float64{44},
	}
	args := batchArgs(spec, batchPlan{seekAt: 40, anchor: 40})
	if got := argVal(t, args, "-c:v"); got != "copy" {
		t.Errorf("-c:v = %q, want copy", got)
	}
	if got := argVal(t, args, "-c:a"); got != "aac" {
		t.Errorf("-c:a = %q, want aac", got)
	}
}

// Segment files must land on the shared name format in the session's dir, and
// the internal playlist must not collide with a segment name.
func TestBatchArgsSegmentPaths(t *testing.T) {
	spec := BatchSpec{Input: "in.mkv", Dir: "/tmp/sess", Count: 4, DurSec: 16, SegDur: 4}
	args := batchArgs(spec, batchPlan{})
	if got := argVal(t, args, "-hls_segment_filename"); got != "/tmp/sess/"+SegPattern {
		t.Errorf("-hls_segment_filename = %q, want the shared SegPattern under Dir", got)
	}
	if got := args[len(args)-1]; got != "/tmp/sess/.batch.m3u8" {
		t.Errorf("output = %q, want the ignored internal playlist", got)
	}

	remux := BatchSpec{Input: "in.mkv", Dir: "/tmp/sess", Count: 1, DurSec: 4, SegDur: 4, CopyVideo: true}
	rargs := batchArgs(remux, batchPlan{})
	if got := rargs[len(rargs)-1]; got != "/tmp/sess/"+SegPattern {
		t.Errorf("remux output = %q, want the segment pattern", got)
	}
}

// planBatch1 is planBatch's no-probe answer: the plan for a spec whose input
// can't be probed (which is every spec in these tests, since no file exists).
// Kept explicit so a test asserting shared invariants doesn't silently depend
// on probe failure.
func planBatch1(spec BatchSpec) batchPlan {
	return batchPlan{seekAt: spec.StartSec, anchor: spec.StartSec}
}
