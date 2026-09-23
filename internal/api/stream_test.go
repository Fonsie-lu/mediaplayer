package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"mediaplayer/internal/session"
	"mediaplayer/internal/transcode"
)

// keyframesEvery returns a keyframe source with one keyframe per gop seconds.
func keyframesEvery(gop, dur float64) func() ([]float64, error) {
	return func() ([]float64, error) {
		var kfs []float64
		for t := 0.0; t < dur; t += gop {
			kfs = append(kfs, t)
		}
		return kfs, nil
	}
}

func h264Probe() *transcode.ProbeResult {
	return &transcode.ProbeResult{
		VCodec: "h264", PixFmt: "yuv420p", Duration: 120,
		AudioTracks: []transcode.AudioTrack{
			{Index: 0, Codec: "ac3", Language: "ger"},
			{Index: 1, Codec: "aac", Language: "eng"},
		},
		PreferredAudio: 1,
	}
}

func TestPlanStreamRemuxesBrowserH264(t *testing.T) {
	p := planStream(h264Probe(), "", "", keyframesEvery(2, 120))
	if !p.CopyVideo || p.mode() != "remux" {
		t.Fatalf("mode = %s, want remux for 8-bit h264 at source quality", p.mode())
	}
	if p.Boundaries[0] != 0 || p.Boundaries[len(p.Boundaries)-1] != 120 {
		t.Errorf("Boundaries = %v, want 0 … duration", p.Boundaries)
	}
	if p.AudioIdx != 1 || !p.CopyAudio {
		t.Errorf("audio = %d copy=%t, want the preferred aac track, copied", p.AudioIdx, p.CopyAudio)
	}
}

func TestPlanStreamFallsBackToTranscode(t *testing.T) {
	hi10 := h264Probe()
	hi10.PixFmt = "yuv420p10le"
	hevc := h264Probe()
	hevc.VCodec = "hevc"
	cases := []struct {
		name      string
		probe     *transcode.ProbeResult
		q         string
		keyframes func() ([]float64, error)
		scans     bool // remux was possible until the keyframes said no
	}{
		// MSE decodes Hi10P no better than a <video src> does.
		{"10-bit h264", hi10, "", keyframesEvery(2, 120), false},
		{"not h264", hevc, "", keyframesEvery(2, 120), false},
		{"quality cap", h264Probe(), "720", keyframesEvery(2, 120), false},
		{"unscannable", h264Probe(), "", func() ([]float64, error) { return nil, errors.New("scan failed") }, true},
		{"sparse keyframes", h264Probe(), "", keyframesEvery(40, 120), true},
	}
	for _, c := range cases {
		scanned := false
		kf := func() ([]float64, error) { scanned = true; return c.keyframes() }
		p := planStream(c.probe, c.q, "", kf)
		if p.CopyVideo || p.Boundaries != nil {
			t.Errorf("%s: mode = %s, want transcode", c.name, p.mode())
		}
		// The scan reads the whole file; it must not run when its answer
		// can't matter.
		if scanned != c.scans {
			t.Errorf("%s: keyframe scan ran = %t, want %t", c.name, scanned, c.scans)
		}
	}
}

func TestPlanStreamQualityAndAudio(t *testing.T) {
	p := planStream(h264Probe(), "480", "0", keyframesEvery(2, 120))
	if p.MaxHeight != 480 {
		t.Errorf("MaxHeight = %d, want 480", p.MaxHeight)
	}
	if p.AudioIdx != 0 || p.CopyAudio {
		t.Errorf("audio = %d copy=%t, want track 0 (ac3) re-encoded", p.AudioIdx, p.CopyAudio)
	}
	// An out-of-range or junk track falls back to the probed preference.
	for _, audio := range []string{"7", "-1", "x"} {
		if p := planStream(h264Probe(), "", audio, keyframesEvery(2, 120)); p.AudioIdx != 1 {
			t.Errorf("audio=%q: AudioIdx = %d, want the preferred 1", audio, p.AudioIdx)
		}
	}
	if p := planStream(h264Probe(), "source", "", keyframesEvery(2, 120)); p.MaxHeight != 0 {
		t.Errorf("q=source: MaxHeight = %d, want no cap", p.MaxHeight)
	}
}

// Segment URLs are per cookie, not per session instance, so a cached response
// would be spliced into the next video or the next quality.
func TestStreamHLSSegmentsNotCacheable(t *testing.T) {
	mux, mgr := testMux(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, transcode.SegName(0)), []byte("ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr.Adopt("sid1", &session.Session{ID: "sid1", Duration: 8, Dir: dir})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/stream/hls/sid1/seg_00000.ts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("segment: status %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}
