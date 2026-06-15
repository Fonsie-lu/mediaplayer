package transcode

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

type AudioTrack struct {
	Index    int    `json:"index"`    // audio-stream-relative index (0-based among audio streams)
	Language string `json:"language"` // ISO 639-2/T language tag, e.g. "eng"
	Title    string `json:"title"`
	Codec    string `json:"codec"`
	Default  bool   `json:"default"`
}

type ProbeResult struct {
	Container   string       `json:"container"`
	VCodec      string       `json:"vcodec"`
	ACodec      string       `json:"acodec"`
	Width       int          `json:"width"`
	Height      int          `json:"height"`
	Duration    float64      `json:"duration"`
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
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Tags      struct {
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

// probeCache memoizes ffprobe results keyed by path + size + mtime. The
// player page probes a file and then opens a stream within the same second,
// which would otherwise run ffprobe twice on every video open.
var probeCache = struct {
	sync.Mutex
	m map[string]*ProbeResult
}{m: map[string]*ProbeResult{}}

const probeCacheMax = 512

func Probe(path string) (*ProbeResult, error) {
	var key string
	if st, err := os.Stat(path); err == nil {
		key = path + "|" + strconv.FormatInt(st.Size(), 10) + "|" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
		probeCache.Lock()
		cached := probeCache.m[key]
		probeCache.Unlock()
		if cached != nil {
			return cached, nil
		}
	}
	r, err := probeUncached(path)
	if err != nil {
		return nil, err
	}
	if key != "" {
		probeCache.Lock()
		if len(probeCache.m) >= probeCacheMax {
			probeCache.m = map[string]*ProbeResult{}
		}
		probeCache.m[key] = r
		probeCache.Unlock()
	}
	return r, nil
}

func probeUncached(path string) (*ProbeResult, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var p ffprobeOut
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, err
	}
	r := &ProbeResult{Container: p.Format.FormatName}
	if d, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil {
		r.Duration = d
	}
	audioIdx := 0
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			if r.VCodec == "" {
				r.VCodec = s.CodecName
				r.Width = s.Width
				r.Height = s.Height
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
	for _, c := range strings.Split(r.Container, ",") {
		if directContainers[c] {
			containerOK = true
			break
		}
	}
	r.Direct = containerOK && directVCodecs[r.VCodec] && (r.ACodec == "" || directACodecs[r.ACodec])
	return r, nil
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
