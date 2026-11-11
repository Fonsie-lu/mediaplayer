package transcode

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type AudioTrack struct {
	Index    int    `json:"index"`    // audio-stream-relative index (0-based among audio streams)
	Language string `json:"language"` // ISO 639-2/T language tag, e.g. "eng"
	Title    string `json:"title"`
	Codec    string `json:"codec"`
	Default  bool   `json:"default"`
}

type ProbeResult struct {
	Container string  `json:"container"`
	VCodec    string  `json:"vcodec"`
	ACodec    string  `json:"acodec"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Duration  float64 `json:"duration"`
	// StartTime is the container's start_time: the source PTS that ffmpeg's
	// -ss counts from. Matroska and mp4 start at (or a few ms off) zero, but an
	// mpegts recording carries the broadcast clock — thousands of seconds —
	// so every raw PTS ffprobe reports has to be rebased by it before it can be
	// compared with a playlist position. See Origin on BatchSpec.
	StartTime float64 `json:"start_time"`
	// PixFmt and Interlaced feed the browser-compatibility checks: 10-bit or
	// 4:2:2/4:4:4 h264 decodes in no browser, and interlaced video shows
	// combing unless the transcode deinterlaces it.
	PixFmt      string       `json:"pix_fmt"`
	Interlaced  bool         `json:"interlaced"`
	Direct      bool         `json:"direct"` // browser can play source without transcode
	AudioTracks []AudioTrack `json:"audio_tracks"`
	// PreferredAudio is the audio-stream-relative index we want to use by
	// default. English-tagged tracks win; otherwise a default-flagged track;
	// otherwise 0.
	PreferredAudio int `json:"preferred_audio"`
}

type ffprobeOut struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		StartTime  string `json:"start_time"`
	} `json:"format"`
	Streams []struct {
		CodecType  string `json:"codec_type"`
		CodecName  string `json:"codec_name"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		PixFmt     string `json:"pix_fmt"`
		FieldOrder string `json:"field_order"`
		Duration   string `json:"duration"`
		Tags       struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`
		Disposition struct {
			Default int `json:"default"`
		} `json:"disposition"`
	} `json:"streams"`
}

var directContainers = map[string]bool{
	"mov": true, "mp4": true, "m4a": true, "3gp": true, "3g2": true, "mj2": true,
	"webm": true, "matroska": true, // matroska often works in Chrome/Firefox if codec is right
}

var directVCodecs = map[string]bool{
	"h264": true, "vp8": true, "vp9": true, "av1": true,
}

var directACodecs = map[string]bool{
	"aac": true, "opus": true, "vorbis": true, "mp3": true,
}

// browserH264PixFmts are the h264 pixel formats browsers decode. h264's High
// 10 / 4:2:2 / 4:4:4 profiles (yuv420p10le and friends — common in anime
// encodes) share the codec name but fail in every browser's decoder, directly
// and through MSE alike, so such a file must be neither served raw nor
// remuxed. VP9 and AV1 decoders handle 10-bit fine and are not restricted.
var browserH264PixFmts = map[string]bool{"yuv420p": true, "yuvj420p": true}

// BrowserDecodableVideo reports whether browsers can decode the probed video
// stream as-is, i.e. whether direct play and remux are open to it at all.
// An unknown pixel format is given the benefit of the doubt: refusing it would
// push every file ffprobe describes incompletely onto a full transcode.
func (r *ProbeResult) BrowserDecodableVideo() bool {
	if !directVCodecs[r.VCodec] {
		return false
	}
	return r.VCodec != "h264" || r.PixFmt == "" || browserH264PixFmts[r.PixFmt]
}

// interlacedOrders are ffprobe's field_order values for interlaced content
// ("progressive" and "unknown" are the others).
var interlacedOrders = map[string]bool{"tt": true, "bb": true, "tb": true, "bt": true}

// probeCache memoizes ffprobe results keyed by path + size + mtime. The
// player page probes a file and then opens a stream within the same second,
// which would otherwise run ffprobe twice on every video open.
var probeCache = newLRU[*ProbeResult](probeCacheMax)

const probeCacheMax = 512

// probeTimeout bounds one ffprobe run. A stalled network mount would
// otherwise hang the request — and the process — for as long as it stays
// stalled; a healthy probe takes well under a second.
const probeTimeout = time.Minute

// Probe describes the file at path. The result is cached and shared: treat it
// as read-only.
func Probe(path string) (*ProbeResult, error) {
	return probeCache.load(statKey(path), func() (*ProbeResult, error) {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, "ffprobe",
			"-v", "error",
			"-print_format", "json",
			"-show_format",
			"-show_streams",
			path,
		).Output()
		if err != nil {
			return nil, err
		}
		return parseProbe(out)
	})
}

// parseProbe turns ffprobe's -show_format -show_streams JSON into a
// ProbeResult, including the direct-play verdict. Pure, so the stream-decision
// inputs can be tested without ffprobe.
func parseProbe(out []byte) (*ProbeResult, error) {
	var p ffprobeOut
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, err
	}
	r := &ProbeResult{Container: p.Format.FormatName}
	if d, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil && d > 0 {
		r.Duration = d
	} else {
		// No container duration ("N/A" for raw elementary streams and some
		// growing recordings): the longest stream's will do, where the
		// alternative is a file that can't be streamed at all.
		for _, st := range p.Streams {
			if d, err := strconv.ParseFloat(st.Duration, 64); err == nil && d > r.Duration {
				r.Duration = d
			}
		}
	}
	if st, err := strconv.ParseFloat(p.Format.StartTime, 64); err == nil {
		r.StartTime = st
	}
	audioIdx := 0
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			// The first video stream, because that is the one every batch maps
			// (0:v:0) and every keyframe scan selects.
			if r.VCodec == "" {
				r.VCodec = s.CodecName
				r.Width = s.Width
				r.Height = s.Height
				r.PixFmt = s.PixFmt
				r.Interlaced = interlacedOrders[s.FieldOrder]
			}
		case "audio":
			if r.ACodec == "" {
				r.ACodec = s.CodecName
			}
			r.AudioTracks = append(r.AudioTracks, AudioTrack{
				Index:    audioIdx,
				Language: strings.ToLower(s.Tags.Language),
				Title:    s.Tags.Title,
				Codec:    s.CodecName,
				Default:  s.Disposition.Default == 1,
			})
			audioIdx++
		}
	}
	r.PreferredAudio = pickPreferredAudio(r.AudioTracks)
	// container is comma-separated list; at least one must be direct
	containerOK := false
	for c := range strings.SplitSeq(r.Container, ",") {
		if directContainers[c] {
			containerOK = true
			break
		}
	}
	r.Direct = containerOK && r.BrowserDecodableVideo() && r.directAudioOK()
	return r, nil
}

// browserAudio is the track a browser plays from the raw file: the
// default-flagged one, else the first. (HTMLMediaElement.audioTracks could
// switch it, but only Safari implements that.)
func browserAudio(tracks []AudioTrack) int {
	for _, t := range tracks {
		if t.Default {
			return t.Index
		}
	}
	return 0
}

// directAudioOK reports whether serving the raw file plays acceptable audio:
// the track the browser picks must be decodable and, when the video could be
// remuxed cheaply instead, be the preferred one — otherwise an mkv whose
// default track is German plays German while the player shows English
// selected. For other video codecs a mismatch is tolerated: the only way to
// switch tracks there is a full re-encode, and the player still tries
// audioTracks and offers the selector.
func (r *ProbeResult) directAudioOK() bool {
	if len(r.AudioTracks) == 0 {
		return true
	}
	played := browserAudio(r.AudioTracks)
	if !directACodecs[r.AudioTracks[played].Codec] {
		return false
	}
	return played == r.PreferredAudio || r.VCodec != "h264"
}

// englishTags covers the common ISO 639 spellings that show up in mkv/mp4 tags.
var englishTags = map[string]bool{"eng": true, "en": true, "english": true}

// pickPreferredAudio picks the best audio track to default to. English wins;
// otherwise a track flagged as default; otherwise the first track.
func pickPreferredAudio(tracks []AudioTrack) int {
	for _, t := range tracks {
		if englishTags[t.Language] {
			return t.Index
		}
	}
	for _, t := range tracks {
		if t.Default {
			return t.Index
		}
	}
	return 0
}
