package api

import (
	"slices"
	"testing"
)

func names(e []FileEntry) []string {
	out := make([]string, len(e))
	for i := range e {
		out[i] = e[i].Name
	}
	return out
}

func TestSortEntriesFoldersFirst(t *testing.T) {
	e := []FileEntry{
		{Name: "b.mkv", Ctime: 2},
		{Name: "dir", IsDir: true, Ctime: 1},
		{Name: "a.mkv", Ctime: 3},
	}
	sortEntries(e, "ctime_desc")
	got := names(e)
	want := []string{"dir", "a.mkv", "b.mkv"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ctime_desc: got %v, want %v", got, want)
		}
	}
}

func TestSortEntriesNameCaseInsensitive(t *testing.T) {
	e := []FileEntry{
		{Name: "Zebra.mkv"},
		{Name: "apple.mkv"},
		{Name: "Mango.mkv"},
	}
	sortEntries(e, "name_asc")
	got := names(e)
	want := []string{"apple.mkv", "Mango.mkv", "Zebra.mkv"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("name_asc: got %v, want %v", got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	if classify("x", true) != "folder" {
		t.Error("dir should be folder")
	}
	if classify("a.MKV", false) != "video" {
		t.Error("extension match should be case-insensitive")
	}
	if classify("a.txt", false) != "other" {
		t.Error("txt should be other")
	}
}

// The comparator has six branches and the two tests above cover two of them;
// this pins the rest, including that an unknown key falls back to ctime_desc.
func TestSortEntriesAllKeys(t *testing.T) {
	// Name, size and ctime order the three files differently on purpose: with
	// one shared ordering, a branch reading the wrong field would still pass.
	// The directory is in every case because folders-first outranks the key.
	base := []FileEntry{
		{Name: "dir", IsDir: true, Size: 4096, Ctime: 5},
		{Name: "b.mkv", Size: 30, Ctime: 3},
		{Name: "C.mkv", Size: 10, Ctime: 1},
		{Name: "a.mkv", Size: 20, Ctime: 2},
	}
	cases := map[string][]string{
		"name_asc":   {"dir", "a.mkv", "b.mkv", "C.mkv"},
		"name_desc":  {"dir", "C.mkv", "b.mkv", "a.mkv"},
		"size_asc":   {"dir", "C.mkv", "a.mkv", "b.mkv"},
		"size_desc":  {"dir", "b.mkv", "a.mkv", "C.mkv"},
		"ctime_asc":  {"dir", "C.mkv", "a.mkv", "b.mkv"},
		"ctime_desc": {"dir", "b.mkv", "a.mkv", "C.mkv"},
		"":           {"dir", "b.mkv", "a.mkv", "C.mkv"}, // unknown = ctime_desc
	}
	for by, want := range cases {
		e := append([]FileEntry(nil), base...)
		sortEntries(e, by)
		if got := names(e); !slices.Equal(got, want) {
			t.Errorf("sort %q: got %v, want %v", by, got, want)
		}
	}
}
