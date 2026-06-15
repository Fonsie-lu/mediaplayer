package api

import "testing"

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
