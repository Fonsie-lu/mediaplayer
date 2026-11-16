package applog

import (
	"fmt"
	"testing"
)

func TestSessionLinesCarryTheirFile(t *testing.T) {
	s := New(100)
	fmt.Fprintln(s, "[session abc] opened path=dir/film.mkv dur=10.0s q= audio=0")
	fmt.Fprintln(s, "[session abc] batch seg 0..15 from 0.00s")
	fmt.Fprintln(s, "mediaplayer listening on http://x")

	got := s.Snapshot()
	if len(got) != 3 {
		t.Fatalf("%d entries, want 3", len(got))
	}
	if got[1].Session != "abc" || got[1].File != "dir/film.mkv" || got[1].Msg != "batch seg 0..15 from 0.00s" {
		t.Errorf("session line parsed as %+v", got[1])
	}
	if got[2].Session != "" || got[2].File != "" {
		t.Errorf("general line parsed as %+v", got[2])
	}
}

func TestPartialWritesJoinIntoOneLine(t *testing.T) {
	s := New(100)
	fmt.Fprint(s, "half a ")
	if n := len(s.Snapshot()); n != 0 {
		t.Fatalf("%d entries before the newline, want 0", n)
	}
	fmt.Fprint(s, "line\nnext\n")
	got := s.Snapshot()
	if len(got) != 2 || got[0].Msg != "half a line" || got[1].Msg != "next" {
		t.Errorf("entries = %+v", got)
	}
}

// Every stream open is its own session, so the session→file map must not keep
// one entry per video ever opened once the lines themselves have scrolled out.
func TestFileMapIsPrunedWithTheBuffer(t *testing.T) {
	const limit = 50
	s := New(limit)
	for i := range 10 * filesSlack {
		fmt.Fprintf(s, "[session s%d] opened path=f%d.mkv dur=1.0s\n", i, i)
	}
	s.mu.Lock()
	n := len(s.files)
	s.mu.Unlock()
	if max := 2*limit + filesSlack + 1; n > max {
		t.Errorf("%d sessions remembered for a %d-line buffer, want at most %d", n, limit, max)
	}
	// The newest session still resolves.
	fmt.Fprintf(s, "[session s%d] closed\n", 10*filesSlack-1)
	got := s.Snapshot()
	if last := got[len(got)-1]; last.File != fmt.Sprintf("f%d.mkv", 10*filesSlack-1) {
		t.Errorf("latest session lost its file: %+v", last)
	}
}
