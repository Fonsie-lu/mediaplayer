package transcode

import "testing"

func TestPickPreferredAudio(t *testing.T) {
	cases := []struct {
		name   string
		tracks []AudioTrack
		want   int
	}{
		{"empty", nil, 0},
		{"english wins over default", []AudioTrack{
			{Index: 0, Language: "ger", Default: true},
			{Index: 1, Language: "eng"},
		}, 1},
		{"default when no english", []AudioTrack{
			{Index: 0, Language: "ger"},
			{Index: 1, Language: "fre", Default: true},
		}, 1},
		{"first as fallback", []AudioTrack{
			{Index: 0, Language: "ger"},
			{Index: 1, Language: "fre"},
		}, 0},
	}
	for _, c := range cases {
		if got := pickPreferredAudio(c.tracks); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// probeJSON is a trimmed ffprobe -show_format -show_streams document.
func probeJSON(format, start, vcodec, pixFmt, fieldOrder, acodec string) []byte {
	return []byte(`{"format":{"format_name":"` + format + `","duration":"120.0","start_time":"` + start + `"},
		"streams":[
			{"codec_type":"video","codec_name":"` + vcodec + `","width":1920,"height":1080,
			 "pix_fmt":"` + pixFmt + `","field_order":"` + fieldOrder + `"},
			{"codec_type":"audio","codec_name":"` + acodec + `","tags":{"language":"eng"}}]}`)
}

func TestParseProbeDirectPlay(t *testing.T) {
	cases := []struct {
		name   string
		doc    []byte
		direct bool
	}{
		{"8-bit h264 mkv", probeJSON("matroska,webm", "0.000", "h264", "yuv420p", "progressive", "aac"), true},
		{"full-range 8-bit h264", probeJSON("mov,mp4,m4a,3gp,3g2,mj2", "0.000", "h264", "yuvj420p", "progressive", "aac"), true},
		// Hi10P shares the codec name and decodes in no browser: serving it raw
		// is a black screen.
		{"10-bit h264", probeJSON("matroska,webm", "0.000", "h264", "yuv420p10le", "progressive", "aac"), false},
		{"4:4:4 h264", probeJSON("matroska,webm", "0.000", "h264", "yuv444p", "progressive", "aac"), false},
		// VP9/AV1 decoders do handle 10-bit.
		{"10-bit vp9", probeJSON("matroska,webm", "0.000", "vp9", "yuv420p10le", "progressive", "opus"), true},
		{"hevc", probeJSON("matroska,webm", "0.000", "hevc", "yuv420p", "progressive", "aac"), false},
		{"mpegts container", probeJSON("mpegts", "5001.379", "h264", "yuv420p", "progressive", "aac"), false},
		{"ac3 audio", probeJSON("matroska,webm", "0.000", "h264", "yuv420p", "progressive", "ac3"), false},
		// ffprobe leaving pix_fmt out must not demote a file to a transcode.
		{"unknown pix_fmt", probeJSON("matroska,webm", "0.000", "h264", "", "progressive", "aac"), true},
	}
	for _, c := range cases {
		r, err := parseProbe(c.doc)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if r.Direct != c.direct {
			t.Errorf("%s: Direct = %t, want %t", c.name, r.Direct, c.direct)
		}
	}
}

// The container start time is what every raw PTS has to be rebased by; for a
// broadcast recording it is thousands of seconds, not zero.
func TestParseProbeStartTimeAndInterlace(t *testing.T) {
	r, err := parseProbe(probeJSON("mpegts", "30001.389978", "mpeg2video", "yuv420p", "tt", "mp2"))
	if err != nil {
		t.Fatal(err)
	}
	if r.StartTime != 30001.389978 {
		t.Errorf("StartTime = %v, want the container start_time", r.StartTime)
	}
	if !r.Interlaced {
		t.Error("field_order tt not reported as interlaced")
	}
	for _, order := range []string{"progressive", "unknown", ""} {
		r, err := parseProbe(probeJSON("mpegts", "0", "h264", "yuv420p", order, "aac"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Interlaced {
			t.Errorf("field_order %q reported as interlaced", order)
		}
	}
}
