package transcode

import (
	"math"
	"testing"
)

// Real framecrc output of a one-packet stream copy: Matroska uses a 1/1000
// timebase, mpegts 1/90000 (with a side-data column on the packet line).
func TestParseFramecrcPTS(t *testing.T) {
	cases := []struct {
		name, out string
		want      float64
	}{
		{"matroska", "#software: Lavf60.16.100\n#tb 0: 1/1000\n#media_type 0: video\n#codec_id 0: h264\n" +
			"#dimensions 0: 640x360\n#sar 0: 1/1\n#stream#, dts,        pts, duration,     size, hash\n" +
			"0,      36000,      36000,       40,    19386, 0x98f6e461\n", 36},
		{"mpegts", "#tb 0: 1/90000\n0,  453546000,  453546000,     3600,    20249, 0x7359ba3c, S=1,        1\n", 5039.4},
	}
	for _, c := range cases {
		got, err := parseFramecrcPTS(c.out)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if math.Abs(got-c.want) > 1e-6 {
			t.Errorf("%s: pts = %v, want %v", c.name, got, c.want)
		}
	}
	for _, bad := range []string{"", "#tb 0: 1/1000\n", "0, 1, 2, 3, 4, 0x0\n", "#tb 0: 1/0\n0, 1, 2, 3\n"} {
		if _, err := parseFramecrcPTS(bad); err == nil {
			t.Errorf("parseFramecrcPTS(%q) succeeded, want an error", bad)
		}
	}
}

func TestFirstKeyframePTS(t *testing.T) {
	got, err := firstKeyframePTS("5081.360000,___,\n5081.400000,K__,\n5083.400000,K__,\n")
	if err != nil || got != 5081.4 {
		t.Errorf("firstKeyframePTS = %v, %v; want 5081.4", got, err)
	}
	if _, err := firstKeyframePTS("1.0,___\n2.0,___\n"); err == nil {
		t.Error("no keyframe in the window must be an error, not a zero landing")
	}
}
