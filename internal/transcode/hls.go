package transcode

import (
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
	"syscall"
	"time"
)

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

	done chan struct{}
	once sync.Once
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

	// CopyVideo enables remux mode: the source h264 stream is copied
	// bit-for-bit and segments split on existing keyframes. CopyAudio
	// likewise passes the audio stream through (codec must be valid in
	// mpegts and browser-decodable, e.g. aac/mp3).
	CopyVideo bool
	CopyAudio bool

	// SplitTimes (remux mode) are the absolute source times of the batch's
	// internal segment boundaries — keyframe timestamps for segments
	// StartSeg+1 .. StartSeg+Count-1.
	SplitTimes []float64
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
func StartBatch(spec BatchSpec) (*Batch, error) {
	if err := os.MkdirAll(spec.Dir, 0o755); err != nil {
		return nil, err
	}
	segPattern := filepath.Join(spec.Dir, "seg_%05d.ts")
	// internal playlist file ffmpeg insists on writing — we ignore it and
	// generate our own VOD playlist from session metadata instead.
	internalPlaylist := filepath.Join(spec.Dir, ".batch.m3u8")

	// Where -ss points and where the batch's content actually begins.
	// Containers with a real keyframe index (matroska cues, mp4 stss) seek to
	// a keyframe at or before the target. Index-less containers (mpegts DVB
	// recordings) binary-search to a byte position, and usable content only
	// begins at the next keyframe — possibly seconds *after* the target. A
	// batch whose first segment is missing its head leaves a buffer hole at
	// the player's seek position that no amount of fetching can fill: hls.js
	// backtracks to n-1, whose own batch has the same defect, and stalls.
	// When the landing overshoots, back the seek off until content starts at
	// or before StartSec; each mode then reconciles the early start exactly.
	seekAt := spec.StartSec
	anchor := spec.StartSec // remux: PTS of the first packet ffmpeg copies
	backoff := 0.0          // encode: seconds decoded then trimmed before StartSec

	if spec.CopyVideo {
		if l, err := probeLanding(spec.Input, spec.Dir, seekAt); err == nil {
			for tries := 0; l > spec.StartSec+0.05 && seekAt > 0 && tries < 3; tries++ {
				seekAt = math.Max(0, seekAt-(l-spec.StartSec)-2.0)
				nl, nerr := probeLanding(spec.Input, spec.Dir, seekAt)
				if nerr != nil {
					break
				}
				l = nl
			}
			if l > spec.StartSec+0.05 {
				log.Printf("remux batch [%d..]: copy seek lands at %.2fs, after segment start %.2fs — first segment will be short",
					spec.StartSeg, l, spec.StartSec)
			}
			anchor = l
		}
	} else if spec.StartSec > 0 {
		if kf, err := keyframeLanding(spec.Input, seekAt); err == nil && kf > spec.StartSec+0.05 {
			l := kf
			for tries := 0; l > spec.StartSec+0.05 && seekAt > 0 && tries < 3; tries++ {
				seekAt = math.Max(0, seekAt-(l-spec.StartSec)-2.0)
				nl, nerr := keyframeLanding(spec.Input, seekAt)
				if nerr != nil {
					break
				}
				l = nl
			}
			if l > spec.StartSec+0.05 {
				log.Printf("encode batch [%d..]: no keyframe at or before segment start %.2fs (decode starts %.2fs) — first segment will be short",
					spec.StartSeg, spec.StartSec, l)
			}
			backoff = spec.StartSec - seekAt
		}
	}

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
		var vf []string
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
	// back makes every batch's PTS equal the source's.
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

	cmd := exec.Command("ffmpeg", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if logFile, err := os.Create(filepath.Join(spec.Dir, fmt.Sprintf("ffmpeg-%05d.log", spec.StartSeg))); err == nil {
		cmd.Stderr = logFile
		cmd.Stdout = logFile
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	b := &Batch{
		Dir:        spec.Dir,
		StartSeg:   spec.StartSeg,
		Count:      spec.Count,
		Cmd:        cmd,
		Sequential: spec.CopyVideo,
		done:       make(chan struct{}),
	}
	go func() {
		werr := cmd.Wait()
		if werr != nil {
			// non-zero exit is expected on Stop (we SIGKILL); don't spam logs
			if _, ok := werr.(*exec.ExitError); !ok {
				log.Printf("ffmpeg batch [%d..%d] wait: %v", spec.StartSeg, spec.StartSeg+spec.Count-1, werr)
			}
			if b.Sequential {
				// The segment muxer writes files in place (no temp_file
				// rename), so an interrupted run leaves its in-progress
				// segment truncated on disk. It is the highest-numbered one
				// this batch produced — drop it before waiters re-check,
				// or a later request would serve it as a cached segment.
				b.removeNewestSegment()
			}
		}
		close(b.done)
	}()
	return b, nil
}

// probeLanding reports the PTS of the keyframe a stream-copy `-ss target`
// actually lands on: ffmpeg seeks the same way the batch will, copies a
// single video frame with timestamps preserved, and we read its PTS back.
// Matroska seeks land on cue points, which can be one or more keyframes
// before the target.
func probeLanding(input, dir string, target float64) (float64, error) {
	tmp := filepath.Join(dir, ".landing.ts")
	defer os.Remove(tmp)
	probe := exec.Command("ffmpeg", "-y", "-v", "error",
		"-ss", strconv.FormatFloat(target, 'f', 3, 64),
		"-i", input,
		"-map", "0:v:0",
		"-c", "copy",
		"-frames:v", "1",
		"-copyts",
		"-muxdelay", "0",
		"-avoid_negative_ts", "disabled",
		"-f", "mpegts", tmp,
	)
	if err := probe.Run(); err != nil {
		return 0, err
	}
	out, err := exec.Command("ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "packet=pts_time",
		"-of", "csv=p=0", tmp,
	).Output()
	if err != nil {
		return 0, err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(first), ","), 64)
}

// keyframeLanding reports the PTS of the first keyframe-flagged video packet
// at or after where a seek to target lands — i.e. the earliest point an
// encode batch's decoded output can begin. Demux-only (no decoding), so it
// costs a few tens of milliseconds. Containers with a keyframe index land at
// a keyframe at/before target; index-less ones (mpegts) can land mid-GOP,
// putting the first decodable frame after target.
func keyframeLanding(input string, target float64) (float64, error) {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-read_intervals", strconv.FormatFloat(target, 'f', 3, 64)+"%+#2000",
		"-select_streams", "v:0",
		"-show_entries", "packet=pts_time,flags",
		"-of", "csv=p=0", input,
	).Output()
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) < 2 || !strings.Contains(fields[1], "K") {
			continue
		}
		if pts, err := strconv.ParseFloat(fields[0], 64); err == nil {
			return pts, nil
		}
	}
	return 0, errors.New("no keyframe within probe window")
}

// removeNewestSegment deletes the highest-numbered segment file in this
// batch's range. Called when a sequential (segment-muxer) batch exits
// abnormally, since its newest file may be truncated mid-write.
func (b *Batch) removeNewestSegment() {
	for n := b.StartSeg + b.Count - 1; n >= b.StartSeg; n-- {
		p := filepath.Join(b.Dir, fmt.Sprintf("seg_%05d.ts", n))
		if _, err := os.Stat(p); err == nil {
			_ = os.Remove(p)
			return
		}
	}
}

// Contains reports whether segment N is inside this batch's range.
func (b *Batch) Contains(n int) bool {
	return n >= b.StartSeg && n < b.StartSeg+b.Count
}

// Done is closed when ffmpeg exits.
func (b *Batch) Done() <-chan struct{} { return b.done }

// Finished reports whether ffmpeg has exited, without blocking.
func (b *Batch) Finished() bool {
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

// Stop signals the batch's ffmpeg process group: SIGTERM first so any
// in-flight segment write gets flushed/closed (releasing tmpfs pages),
// SIGKILL after a short grace period if it hasn't exited.
func (b *Batch) Stop() {
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
