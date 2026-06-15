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
