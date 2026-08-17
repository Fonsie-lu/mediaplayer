package transcode

import (
	"errors"
	"testing"
)

func TestSegNameRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 9, 10, 99999} {
		name := SegName(n)
		got, ok := ParseSegName(name)
		if !ok {
			t.Fatalf("ParseSegName(%q) rejected a name SegName produced", name)
		}
		if got != n {
			t.Errorf("ParseSegName(%q) = %d, want %d", name, got, n)
		}
	}
	if got := SegName(7); got != "seg_00007.ts" {
		t.Errorf("SegName(7) = %q, want seg_00007.ts", got)
	}
}

func TestParseSegNameRejects(t *testing.T) {
	// The HLS handler feeds this untrusted path segments, so anything that
	// isn't a segment name must come back false rather than a zero index.
	for _, name := range []string{
		"playlist.m3u8",
		"seg_00001.tsx",
		"xseg_00001.ts",
		"seg_.ts",
		"seg_abc.ts",
		"seg_-1.ts",
		"seg_00001.ts/../etc",
		".batch.m3u8",
		"",
	} {
		if n, ok := ParseSegName(name); ok {
			t.Errorf("ParseSegName(%q) = (%d, true), want false", name, n)
		}
	}
}

// correctSeek is best-effort: a failing first probe must leave the requested
// position untouched rather than moving the batch, and a probe that keeps
// overshooting must give up instead of walking to zero.
func TestCorrectSeek(t *testing.T) {
	t.Run("landing already at or before want", func(t *testing.T) {
		calls := 0
		at, l, ok := correctSeek(100, func(float64) (float64, error) {
			calls++
			return 99.5, nil
		})
		if !ok || at != 100 || l != 99.5 {
			t.Fatalf("got (%v, %v, %v), want (100, 99.5, true)", at, l, ok)
		}
		if calls != 1 {
			t.Errorf("probed %d times, want 1 — no back-off needed", calls)
		}
	})

	t.Run("first probe fails", func(t *testing.T) {
		at, _, ok := correctSeek(100, func(float64) (float64, error) {
			return 0, errors.New("nope")
		})
		if ok {
			t.Error("ok = true, want false when the probe fails")
		}
		if at != 100 {
			t.Errorf("seekAt = %v, want the uncorrected 100", at)
		}
	})

	t.Run("overshoot converges", func(t *testing.T) {
		// Landing tracks the seek position: once we back off, content starts
		// before want.
		at, l, ok := correctSeek(100, func(seek float64) (float64, error) {
			if seek >= 100 {
				return 104, nil // overshoots by 4s
			}
			return seek, nil
		})
		if !ok {
			t.Fatal("ok = false")
		}
		if at >= 100 || l > 100 {
			t.Errorf("got seekAt=%v landing=%v, want both backed off before 100", at, l)
		}
	})

	t.Run("hopeless overshoot gives up", func(t *testing.T) {
		calls := 0
		_, l, ok := correctSeek(100, func(float64) (float64, error) {
			calls++
			return 200, nil // always lands late, whatever we ask for
		})
		if !ok {
			t.Fatal("ok = false — probes succeeded, so the walk reports its result")
		}
		if l <= 100 {
			t.Errorf("landing = %v, want the still-late value so the caller can warn", l)
		}
		if calls > maxSeekTries+1 {
			t.Errorf("probed %d times, want at most %d", calls, maxSeekTries+1)
		}
	})
}
