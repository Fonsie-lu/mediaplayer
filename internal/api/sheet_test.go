package api

import "testing"

func TestSheetTimes(t *testing.T) {
	cases := []struct {
		name string
		dur  float64
		want []float64
	}{
		// shorter than one block: a single frame from the middle
		{"short clip", 90, []float64{45}},
		// 25 min → three 10-minute blocks, the last one short
		{"partial last block", 1500, []float64{300, 900, 1350}},
		// exact multiple: no zero-length trailing block
		{"exact multiple", 1200, []float64{300, 900}},
		{"zero duration", 0, nil},
		{"negative duration", -5, nil},
	}
	for _, c := range cases {
		got := sheetTimes(c.dur)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

func TestSheetTimesSpacing(t *testing.T) {
	// every frame must fall inside the video and one interval apart
	times := sheetTimes(8771.2)
	if len(times) != 15 {
		t.Fatalf("got %d frames for 2h26m, want 15", len(times))
	}
	for i, tt := range times {
		if tt <= 0 || tt >= 8771.2 {
			t.Errorf("frame %d at %v is outside the video", i, tt)
		}
		if i > 0 && times[i-1] >= tt {
			t.Errorf("frame %d at %v is not after %v", i, tt, times[i-1])
		}
	}
}

func TestHHMMSS(t *testing.T) {
	cases := map[float64]string{
		0:      "00:00:00",
		45:     "00:00:45",
		59.6:   "00:01:00", // rounds, so ffmpegthumbnailer never sees "00:00:60"
		300:    "00:05:00",
		3600:   "01:00:00",
		8770.5: "02:26:11",
		-5:     "00:00:00",
	}
	for in, want := range cases {
		if got := hhmmss(in); got != want {
			t.Errorf("hhmmss(%v) = %q, want %q", in, got, want)
		}
	}
}
