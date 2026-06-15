package transcode

import (
	"math"
	"testing"
)

func TestParseKeyframeCSV(t *testing.T) {
	out := "0.000000,K__\n" +
		"0.040000,___\n" +
		"N/A,K__\n" +
		"4.200000,K__\n" +
		"2.100000,K__\n" + // out of order — must be sorted
		"\n"
	got := parseKeyframeCSV(out)
	want := []float64{0, 2.1, 4.2}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestBuildBoundaries(t *testing.T) {
	// keyframes every 2s, 20s video, 4s target → boundaries every 4s
	var kfs []float64
	for ts := 0.0; ts < 20; ts += 2 {
		kfs = append(kfs, ts)
	}
	b := BuildBoundaries(kfs, 20, 4)
	want := []float64{0, 4, 8, 12, 16, 20}
	if len(b) != len(want) {
		t.Fatalf("got %v, want %v", b, want)
	}
	for i := range want {
		if math.Abs(b[i]-want[i]) > 1e-9 {
			t.Fatalf("got %v, want %v", b, want)
		}
	}
}

func TestBuildBoundariesIrregular(t *testing.T) {
	kfs := []float64{0, 3.0, 9.5, 10.0, 17.2}
	b := BuildBoundaries(kfs, 20, 4)
	// first kf ≥ 4 after 0 is 9.5; after 9.5 is 17.2
	want := []float64{0, 9.5, 17.2, 20}
	if len(b) != len(want) {
		t.Fatalf("got %v, want %v", b, want)
	}
	for i := range want {
		if math.Abs(b[i]-want[i]) > 1e-9 {
			t.Fatalf("got %v, want %v", b, want)
		}
	}
}

func TestBuildBoundariesFoldsTinyTail(t *testing.T) {
	kfs := []float64{0, 4, 8, 11.8}
	b := BuildBoundaries(kfs, 12.1, 4) // 11.8 boundary leaves a 0.3s stub
	last := b[len(b)-1]
	prev := b[len(b)-2]
	if last != 12.1 {
		t.Fatalf("last boundary must be duration, got %v", b)
	}
	if last-prev < 1.0 {
		t.Fatalf("tiny tail segment not folded: %v", b)
	}
}

func TestMaxGap(t *testing.T) {
	if g := MaxGap([]float64{0, 4, 30, 31}); g != 26 {
		t.Fatalf("MaxGap = %v, want 26", g)
	}
	if g := MaxGap([]float64{0}); g != 0 {
		t.Fatalf("MaxGap single = %v, want 0", g)
	}
}
