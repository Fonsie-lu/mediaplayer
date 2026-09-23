package transcode

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// SegPattern is the segment filename format: ffmpeg's output pattern, the
// name the session checks for completeness, and the name the HLS handler
// parses back are all this one format. It lives here because transcode is the
// package the others import, and a literal repeated in three packages would
// let a rename silently produce segments nobody looks for (the same reasoning
// as session.SessTempPrefix) instead of failing to compile.
const SegPattern = "seg_%05d.ts"

const segPrefix, segSuffix = "seg_", ".ts"

// SegName is the on-disk filename of segment n.
func SegName(n int) string { return fmt.Sprintf(SegPattern, n) }

// ParseSegName is SegName's inverse, reporting false for anything that isn't a
// segment filename.
func ParseSegName(name string) (int, bool) {
	if !strings.HasPrefix(name, segPrefix) || !strings.HasSuffix(name, segSuffix) {
		return 0, false
	}
	// Atoi accepts a leading "-", so reject negatives explicitly: this is the
	// gate an untrusted URL path segment passes through, and a negative index
	// should read as "not a segment name" rather than reach a range check.
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, segPrefix), segSuffix))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// Batch is a short-lived ffmpeg invocation that emits a contiguous range of
// HLS segments [StartSeg, StartSeg+Count). Each batch covers ~1 min of
// video — when the client seeks elsewhere, the running batch is stopped and
// a fresh one spawned at the new offset. This keeps RAM bounded (segments
// outside the active window get evicted by the session) and honors the
// spec's "do not transcode the whole video at once" rule.
type Batch struct {
	Dir      string
	StartSeg int
	Count    int
	Cmd      *exec.Cmd

	// Sequential marks batches produced by the segment muxer (remux mode),
	// which has no temp_file flag: a segment file is only known-complete
	// once its successor exists or ffmpeg has exited. The hls muxer
	// (encode mode) renames segments into place atomically instead.
	Sequential bool

	started  time.Time   // before ffmpeg ran: older files in range predate it
	stopping atomic.Bool // Stop was called; see Stopping
	done     chan struct{}
	once     sync.Once
}

// BatchSpec describes one ffmpeg batch.
//
// StartSec/DurSec are given explicitly rather than derived from StartSeg
// because remux mode splits on source keyframes, so segments are not on a
// uniform SegDur grid.
type BatchSpec struct {
	Input    string
	Dir      string
	StartSeg int     // index of the first emitted segment
	Count    int     // number of segments this batch produces
	StartSec float64 // source time of the first segment
	DurSec   float64 // total batch duration
	SegDur   float64 // target segment duration (split threshold)

	MaxHeight int // 0 = keep source resolution (encode mode only)
	AudioIdx  int // audio-stream-relative index to map

	// Origin is the source's container start_time (ProbeResult.StartTime).
	// Every time in a spec — StartSec, SplitTimes, the playlist — is measured
	// from it, because that is what -ss counts from; the landing probes see
	// raw PTS and subtract it. Zero-ish for Matroska/mp4, the broadcast clock
	// for an mpegts recording.
	Origin float64

	// Deinterlace (encode mode only) runs the video through bwdif first.
	// Browsers display interlaced frames as-is, combing and all.
	Deinterlace bool

	// CopyVideo enables remux mode: the source h264 stream is copied
	// bit-for-bit and segments split on existing keyframes. CopyAudio
	// likewise passes the audio stream through (codec must be valid in
	// mpegts and browser-decodable, e.g. aac/mp3).
	CopyVideo bool
	CopyAudio bool

	// SplitTimes (remux mode) are the source times (from Origin) of the
	// batch's internal segment boundaries — keyframe timestamps for segments
	// StartSeg+1 .. StartSeg+Count-1.
	SplitTimes []float64
}

// batchPlan is everything StartBatch decides before it can build an ffmpeg
// command line: where to seek, where the content that seek reaches actually
// begins, and how much of it must be trimmed back off.
//
// Splitting it out is what makes the command line testable — planning needs
// ffmpeg probes against a real file, building the args from a plan needs
// nothing at all.
type batchPlan struct {
	// seekAt is the -ss value: StartSec, or earlier when the landing overshot.
	seekAt float64
	// anchor (remux) is the PTS of the first packet ffmpeg copies. Segment
	// split times are measured from it, since that is where the muxer starts
	// counting.
	anchor float64
	// backoff (encode) is how many seconds before StartSec decoding begins, to
	// be cut back off by the trim filter.
	backoff float64
}

// planBatch decides the seek correction for spec by probing the input.
//
// Where -ss lands is container-dependent. Containers with a real keyframe
// index (matroska cues, mp4 stss) seek to a keyframe at or before the target.
// Index-less containers (mpegts DVB recordings) binary-search to a byte
// position, and usable content only begins at the next keyframe — possibly
// seconds *after* the target. A batch whose first segment is missing its head
// leaves a buffer hole at the player's seek position that no amount of
// fetching can fill: hls.js backtracks to n-1, whose own batch has the same
// defect, and stalls. When the landing overshoots, back the seek off until
// content starts at or before StartSec; each mode then reconciles the early
// start exactly (see batchArgs).
//
// The two modes differ only in how they probe (a copy replay vs a keyframe
// scan) and in how they reconcile the early start, so the back-off walk itself
// is shared — see correctSeek. Both probes are best-effort: on error the plan
// is the uncorrected seek.
func planBatch(spec BatchSpec) batchPlan {
	p := batchPlan{seekAt: spec.StartSec, anchor: spec.StartSec}
	if spec.CopyVideo {
		if s, l, ok := correctSeek(spec.StartSec, func(at float64) (float64, error) {
			return probeLanding(spec.Input, at, spec.Origin)
		}); ok {
			p.seekAt = s
			if l > spec.StartSec+landingSlop {
				log.Printf("remux batch [%d..]: copy seek lands at %.2fs, after segment start %.2fs — first segment will be short",
					spec.StartSeg, l, spec.StartSec)
			}
			p.anchor = l
		}
		return p
	}
	if spec.StartSec > 0 {
		if s, l, ok := correctSeek(spec.StartSec, func(at float64) (float64, error) {
			return keyframeLanding(spec.Input, at, spec.Origin)
		}); ok {
			p.seekAt = s
			if l > spec.StartSec+landingSlop {
				log.Printf("encode batch [%d..]: no keyframe at or before segment start %.2fs (decode starts %.2fs) — first segment will be short",
					spec.StartSeg, spec.StartSec, l)
			}
			p.backoff = spec.StartSec - p.seekAt
		}
	}
	return p
}

// timelinePad is added to every batch's output timestamps, so that none of
// them can go negative. B-frames give an h264 stream (copied or x264's own)
// a first DTS a frame or two *before* its first PTS, and AAC priming puts the
// first audio packet 21ms early. On a batch whose timeline starts at 0 — the
// batch at the head of the video, and no other — those came out negative,
// make_non_negative shifted that batch alone ~80-170ms late, and its seam
// with the next batch played as an overlap. Players map a stream's PTS onto
// the playlist from the first fragment they load, so a constant offset is
// invisible to them; only a per-batch one is not. Anything comfortably above
// a GOP's decode delay would do.
const timelinePad = 10.0

// batchArgs builds ffmpeg's full argument list for spec under plan. Pure: no
// filesystem access, no subprocesses — the streaming path's trickiest logic
// (which muxer, what gets copied, how the timeline is rebased) reduced to a
// value a test can assert on.
func batchArgs(spec BatchSpec, plan batchPlan) []string {
	segPattern := filepath.Join(spec.Dir, SegPattern)
	// internal playlist file ffmpeg insists on writing — we ignore it and
	// generate our own VOD playlist from session metadata instead.
	internalPlaylist := filepath.Join(spec.Dir, ".batch.m3u8")
	seekAt, anchor, backoff := plan.seekAt, plan.anchor, plan.backoff

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-ss", strconv.FormatFloat(seekAt, 'f', 3, 64),
		"-i", spec.Input,
		// Copy mode measures -t from the first copied packet (the landing
		// keyframe), so extend it by the landing slop or the batch tail
		// would come up short. Encode mode trims to StartSec first, so
		// DurSec is already exact there.
		"-t", strconv.FormatFloat(spec.DurSec+(spec.StartSec-anchor), 'f', 3, 64),
		"-map", "0:v:0?",
		"-map", fmt.Sprintf("0:a:%d?", spec.AudioIdx),
	}
	if spec.CopyVideo {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args,
			"-c:v", "libx264",
			// LAN-only target: bitrate is free, CPU is not. A low CRF with a
			// fast preset buys quality with bits instead of encode time,
			// keeping batches comfortably faster than realtime.
			"-preset", "veryfast",
			"-crf", "19",
			"-pix_fmt", "yuv420p",
			"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%.3f)", spec.SegDur),
		)
		// With a backoff, decode starts early and the trim filter cuts the
		// output back to exactly StartSec; setpts shifts the timeline so the
		// downstream args (-t, force_key_frames, -output_ts_offset) see the
		// same 0-at-StartSec timebase as the no-backoff path.
		//
		// Deinterlacing goes first, on the source frames: bwdif is temporal,
		// so it wants its neighbours before the trim discards any. deint=
		// interlaced leaves frames not flagged as interlaced untouched, which
		// makes it safe on streams that mix the two (DVB ad breaks).
		var vf []string
		if spec.Deinterlace {
			vf = append(vf, "bwdif=mode=send_frame:deint=interlaced")
		}
		if backoff > 0 {
			vf = append(vf,
				fmt.Sprintf("trim=start=%.3f", backoff),
				fmt.Sprintf("setpts=PTS-%.3f/TB", backoff),
			)
		}
		if spec.MaxHeight > 0 {
			vf = append(vf, fmt.Sprintf("scale=-2:'min(%d,ih)':flags=lanczos", spec.MaxHeight))
		}
		if len(vf) > 0 {
			args = append(args, "-vf", strings.Join(vf, ","))
		}
	}
	if spec.CopyAudio && backoff == 0 {
		args = append(args, "-c:a", "copy")
	} else {
		// Copied audio can't be trimmed by a filter, so a backoff forces an
		// audio re-encode even for browser-compatible codecs.
		args = append(args,
			"-c:a", "aac",
			"-ac", "2",
			"-b:a", "192k",
		)
		if backoff > 0 {
			args = append(args, "-af",
				fmt.Sprintf("atrim=start=%.3f,asetpts=PTS-%.3f/TB", backoff, backoff))
		}
	}
	// Restore the global timeline: input timestamps were shifted down by the
	// -ss value (and by the trim/setpts backoff in encode mode), adding it
	// back makes every batch's PTS equal the source's — plus timelinePad, the
	// same for every batch, so all batches still share one timeline.
	//
	// -muxdelay 0 plus make_non_negative stops mpegts from adding its default
	// 1.4s start offset. Without this, every segment's content begins 1.4s
	// after its playlist position — after a seek the fetched segment then
	// doesn't cover the playhead, and hls.js backtracks to fetch n-1, n,
	// n+1 out of order for every seek.
	tsOffset := seekAt // remux: undo the -ss rebase exactly
	if !spec.CopyVideo {
		tsOffset = spec.StartSec // encode: timeline is 0 at StartSec post-trim
	}
	tsOffset += timelinePad
	args = append(args,
		"-output_ts_offset", strconv.FormatFloat(tsOffset, 'f', 3, 64),
		"-muxdelay", "0",
		"-avoid_negative_ts", "make_non_negative",
	)

	if spec.CopyVideo {
		// Remux mode uses the segment muxer because it accepts explicit
		// split times. An input -ss with -c copy lands on *some* keyframe at
		// or before the target (matroska cue granularity) and those early
		// packets can't be decoded away — with the hls muxer's count-based
		// splitting they would shift every segment of the batch. Explicit
		// split times instead absorb the slop into the first segment, which
		// simply starts a little early (PTS stay source-true, players align
		// by timestamp). Splits happen at the first keyframe at/after each
		// time, and every split time IS a keyframe time, so the cut is exact.
		//
		// The muxer measures split times from the batch's first video packet
		// — the landing keyframe — so they must be anchored to where the
		// seek actually lands, not to the requested position. probeLanding
		// (run above to validate the seek) reports the landed keyframe's PTS.
		args = append(args,
			"-f", "segment",
			"-segment_format", "mpegts",
			"-segment_start_number", strconv.Itoa(spec.StartSeg),
		)
		if len(spec.SplitTimes) > 0 {
			a := anchor
			if a >= spec.SplitTimes[0] {
				a = spec.StartSec // probe failed or landed absurdly late
			}
			parts := make([]string, len(spec.SplitTimes))
			for i, t := range spec.SplitTimes {
				parts[i] = strconv.FormatFloat(t-a, 'f', 3, 64)
			}
			args = append(args, "-segment_times", strings.Join(parts, ","))
		} else {
			// single-segment batch: disable time-based splitting entirely
			args = append(args, "-segment_time", "999999")
		}
		args = append(args, segPattern)
	} else {
		args = append(args,
			"-f", "hls",
			"-hls_time", strconv.FormatFloat(spec.SegDur, 'f', 3, 64),
			"-hls_list_size", "0",
			"-hls_segment_type", "mpegts",
			"-hls_segment_filename", segPattern,
			"-start_number", strconv.Itoa(spec.StartSeg),
			"-hls_flags", "temp_file+independent_segments+omit_endlist",
			internalPlaylist,
		)
	}
	return args
}

// StartBatch spawns ffmpeg to produce segments seg_<StartSeg>..seg_<StartSeg+Count-1>
// in spec.Dir. Each segment's PTS is offset by StartSec so that segments
// produced by different batches share a single global timeline (no
// EXT-X-DISCONTINUITY needed at batch boundaries).
//
// `-ss` is given as an input option with default accurate-seek, so the first
// frame produced corresponds exactly to source second StartSec. In remux
// mode StartSec is itself a keyframe timestamp, so the copied stream starts
// exactly there.
//
// In encode mode `-force_key_frames` ensures every segment begins with an
// IDR frame, which HLS requires for independent decoding. In copy mode the
// muxer splits at the source's own keyframes — the same boundaries the
// session's playlist was built from.
//
// The work is three steps: planBatch probes where the seek lands, batchArgs
// turns spec+plan into a command line (pure, so it is the tested part), and
// this function spawns it and watches the process.
func StartBatch(spec BatchSpec) (*Batch, error) {
	if err := os.MkdirAll(spec.Dir, 0o755); err != nil {
		return nil, err
	}
	args := batchArgs(spec, planBatch(spec))

	cmd := exec.Command("ffmpeg", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logPath := filepath.Join(spec.Dir, fmt.Sprintf("ffmpeg-%05d.log", spec.StartSeg))
	if logFile, err := os.Create(logPath); err == nil {
		cmd.Stderr = logFile
		cmd.Stdout = logFile
		defer logFile.Close() // the child holds its own descriptor
	}
	b := &Batch{
		Dir:        spec.Dir,
		StartSeg:   spec.StartSeg,
		Count:      spec.Count,
		Cmd:        cmd,
		Sequential: spec.CopyVideo,
		started:    time.Now(),
		done:       make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		werr := cmd.Wait()
		if werr != nil {
			var exit *exec.ExitError
			switch {
			case !errors.As(werr, &exit):
				log.Printf("ffmpeg batch [%d..%d] wait: %v", spec.StartSeg, spec.StartSeg+spec.Count-1, werr)
			case !b.stopping.Load():
				// Not our signal, so ffmpeg itself gave up. Its log is in a
				// session dir that is about to be deleted; its last line is
				// what explains the failure.
				log.Printf("ffmpeg batch [%d..%d] failed (%v): %s", spec.StartSeg, spec.StartSeg+spec.Count-1,
					werr, lastLine(logPath))
			}
			// Whatever ended it, the segment it was writing may be cut short
			// under its final name — drop it before waiters re-check.
			b.discardPartial()
		}
		close(b.done)
	}()
	return b, nil
}

// lastLine returns the last non-empty line of the file at path, or "" when it
// can't be read.
func lastLine(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

const (
	// landingSlop is how far past a segment's start a probed landing may sit
	// before it counts as an overshoot — a keyframe a few ms late leaves no
	// hole worth re-probing for.
	landingSlop = 0.05
	// maxSeekTries bounds the back-off walk: each try costs a probe, and a
	// container that hasn't converged by then won't with more attempts.
	maxSeekTries = 3
)

// correctSeek walks a seek position backwards until the content it lands on
// starts at or before want, returning the corrected position and where the
// last probe landed. probe reports the source time at which a seek to a given
// position actually begins producing usable content; both modes have one (a
// 1-frame copy replay for remux, a keyframe scan for encode) and the walk is
// identical, so only the probe differs.
//
// ok is false when the first probe fails — best-effort by design, the caller
// then proceeds with the uncorrected seek. A returned landing still after want
// means the walk gave up; the caller decides what to do about the short first
// segment.
func correctSeek(want float64, probe func(float64) (float64, error)) (seekAt, landing float64, ok bool) {
	seekAt = want
	l, err := probe(seekAt)
	if err != nil {
		return want, 0, false
	}
	// Overshoot by (l-want) plus 2s of slack, since the next keyframe back is
	// an unknown distance earlier.
	for tries := 0; l > want+landingSlop && seekAt > 0 && tries < maxSeekTries; tries++ {
		seekAt = math.Max(0, seekAt-(l-want)-2.0)
		nl, nerr := probe(seekAt)
		if nerr != nil {
			break
		}
		l = nl
	}
	return seekAt, l, true
}

// landingProbeTimeout bounds each landing probe. They run while the session
// holds its batch-start lock, so one hung on a stalled mount would wedge every
// later seek in that session; a healthy probe takes tens of milliseconds.
const landingProbeTimeout = 20 * time.Second

// probeLanding reports where a stream-copy `-ss target` actually lands, as a
// time measured from origin: ffmpeg seeks exactly the way the batch will,
// copies a single video frame with timestamps preserved, and prints it as a
// framecrc line — the packet's raw PTS plus its stream timebase — to stdout.
// Matroska seeks land on cue points, which can be one or more keyframes before
// the target.
//
// framecrc keeps this to one process with no scratch file. It used to be an
// ffmpeg run writing a one-frame .ts and an ffprobe run reading it back: two
// more opens of the source over a possibly slow mount, per probe, up to
// 1+maxSeekTries probes per seek.
func probeLanding(input string, target, origin float64) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), landingProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error",
		"-ss", strconv.FormatFloat(target, 'f', 3, 64),
		"-i", input,
		"-map", "0:v:0",
		"-c", "copy",
		"-frames:v", "1",
		"-copyts",
		"-avoid_negative_ts", "disabled",
		"-f", "framecrc", "-",
	).Output()
	if err != nil {
		return 0, err
	}
	pts, err := parseFramecrcPTS(string(out))
	if err != nil {
		return 0, err
	}
	return pts - origin, nil
}

// parseFramecrcPTS reads the first packet's PTS, in seconds, out of framecrc
// output: a "#tb 0: num/den" header, then one
// "stream, dts, pts, duration, size, crc[, side data…]" line per packet with
// timestamps in that timebase.
func parseFramecrcPTS(out string) (float64, error) {
	num, den := int64(0), int64(0)
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "#tb 0:"); ok {
			n, d, ok := strings.Cut(strings.TrimSpace(rest), "/")
			if !ok {
				return 0, fmt.Errorf("framecrc: bad timebase %q", rest)
			}
			var err1, err2 error
			num, err1 = strconv.ParseInt(n, 10, 64)
			den, err2 = strconv.ParseInt(d, 10, 64)
			if err1 != nil || err2 != nil || den == 0 {
				return 0, fmt.Errorf("framecrc: bad timebase %q", rest)
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 3 || den == 0 {
			return 0, fmt.Errorf("framecrc: unexpected line %q", line)
		}
		pts, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("framecrc: bad pts in %q", line)
		}
		return float64(pts) * float64(num) / float64(den), nil
	}
	return 0, errors.New("framecrc: no packet")
}

// keyframeLanding reports the first keyframe-flagged video packet at or after
// where a seek to target lands, measured from origin — i.e. the earliest
// point an encode batch's decoded output can begin. Demux-only (no decoding),
// so it costs a few tens of milliseconds. Containers with a keyframe index
// land at a keyframe at/before target; index-less ones (mpegts) can land
// mid-GOP, putting the first decodable frame after target.
//
// The interval is given in its "+offset" form, which ffprobe measures from the
// container start the same way ffmpeg's -ss does. The bare form is an absolute
// PTS: for an mpegts recording whose clock starts at, say, 5000s, every seek
// resolved to the head of the file, the back-off walk gave up at 0, and each
// batch decoded — and threw away — everything from the start of the recording
// up to the seek point.
func keyframeLanding(input string, target, origin float64) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), landingProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-read_intervals", "+"+strconv.FormatFloat(target, 'f', 3, 64)+"%+#2000",
		"-select_streams", "v:0",
		"-show_entries", "packet=pts_time,flags",
		"-of", "csv=p=0", input,
	).Output()
	if err != nil {
		return 0, err
	}
	pts, err := firstKeyframePTS(string(out))
	if err != nil {
		return 0, err
	}
	return pts - origin, nil
}

// firstKeyframePTS returns the first keyframe-flagged entry of ffprobe's
// `pts_time,flags` CSV.
func firstKeyframePTS(out string) (float64, error) {
	for line := range strings.Lines(out) {
		ptsStr, flags, ok := strings.Cut(strings.TrimSpace(line), ",")
		if !ok || !strings.Contains(flags, "K") {
			continue
		}
		if pts, err := strconv.ParseFloat(ptsStr, 64); err == nil {
			return pts, nil
		}
	}
	return 0, errors.New("no keyframe within probe window")
}

// discardPartial deletes what an interrupted batch may have left half-written:
// the last segment file it wrote, and any temp file of the hls muxer's.
//
// Both muxers can leave one. The segment muxer writes in place, so its
// in-progress file is simply truncated. The hls muxer's temp_file flag does not
// save it either: on SIGTERM ffmpeg writes its trailer, and the trailer renames
// the in-progress segment into place — a 0.8s file under a name the playlist
// says is 4s, which encode mode's "present = complete" rule would then serve,
// leaving a hole in the player's buffer. (Only a SIGKILL leaves it as .tmp.)
//
// "Last written" is the newest mtime in the batch's range, not the highest
// index: encode mode doesn't clear its range first, so files from earlier
// batches can sit above the one this batch was writing. Anything older than
// the batch's start is one of those and is left alone. Deleting a segment that
// was in fact complete is harmless — it is regenerated on demand.
func (b *Batch) discardPartial() {
	newest, newestAt := "", b.started
	for n := b.StartSeg; n < b.StartSeg+b.Count; n++ {
		p := filepath.Join(b.Dir, SegName(n))
		if st, err := os.Stat(p); err == nil && !st.ModTime().Before(newestAt) {
			newest, newestAt = p, st.ModTime()
		}
	}
	if newest != "" {
		_ = os.Remove(newest)
	}
	tmps, _ := filepath.Glob(filepath.Join(b.Dir, "seg_*.ts.tmp"))
	for _, p := range tmps {
		_ = os.Remove(p)
	}
}

// Contains reports whether segment N is inside this batch's range.
func (b *Batch) Contains(n int) bool {
	return n >= b.StartSeg && n < b.StartSeg+b.Count
}

// Done is closed when ffmpeg exits.
func (b *Batch) Done() <-chan struct{} { return b.done }

// Stopping reports whether Stop has been called. From then until Finished, no
// file in the batch's range can be trusted: ffmpeg's signal handling may still
// be finalizing a partial segment, and exit cleanup (discardPartial) has not
// run yet.
func (b *Batch) Stopping() bool { return b.stopping.Load() }

// Finished reports whether ffmpeg has exited, without blocking. Exit cleanup
// has run by then.
func (b *Batch) Finished() bool {
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

// Stop signals the batch's ffmpeg process group: SIGTERM first so ffmpeg
// closes its files, SIGKILL after a short grace period if it hasn't exited.
// Either way the segment it was writing is incomplete, and exit cleanup
// (discardPartial) removes it; Stopping covers the window until then. Stop
// returns once ffmpeg has exited, or after the grace periods run out.
func (b *Batch) Stop() {
	b.stopping.Store(true)
	b.once.Do(func() {
		if b.Cmd == nil || b.Cmd.Process == nil {
			return
		}
		pgid, pgErr := syscall.Getpgid(b.Cmd.Process.Pid)
		if pgErr == nil {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		} else {
			_ = b.Cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-b.done:
			return
		case <-time.After(750 * time.Millisecond):
		}
		if pgErr == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = b.Cmd.Process.Kill()
		}
		select {
		case <-b.done:
		case <-time.After(2 * time.Second):
		}
	})
}
